package magitrickle

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"magitrickle/utils/netfilterTools"

	"github.com/miekg/dns"
	"github.com/rs/zerolog/log"
)

var hexDigits = []byte("0123456789abcdef")

// ipset's maximum finite timeout, in seconds (see ipset's timeout option).
const maxRoutingTTL = 2147483

func routingTTL(ttl uint32, additional time.Duration) uint32 {
	seconds := uint64(ttl)
	if additional > 0 {
		seconds += uint64(additional / time.Second)
		if additional%time.Second != 0 {
			seconds++
		}
	}
	if seconds == 0 {
		// DNS TTL=0 must not become an ipset entry with a permanent lifetime.
		return 1
	}
	if seconds > maxRoutingTTL {
		return maxRoutingTTL
	}
	return uint32(seconds)
}

func formatID(id uint16) string {
	return string([]byte{
		hexDigits[id>>12&0xf],
		hexDigits[id>>8&0xf],
		hexDigits[id>>4&0xf],
		hexDigits[id&0xf],
	})
}

func trimFQDN(name string) string {
	if len(name) > 0 && name[len(name)-1] == '.' {
		return name[:len(name)-1]
	}
	return name
}

// ipset interprets timeout=0 as permanent, so round a live fractional TTL up.
func remainingTTL(deadline, now time.Time) uint32 {
	duration := deadline.Sub(now)
	if duration <= 0 {
		return 0
	}
	return routingTTL(0, duration)
}

func (a *App) startDNSListeners(ctx context.Context, errChan chan error) {
	go func() {
		addr, err := net.ResolveUDPAddr("udp", a.Config().DNSProxy.Host.HostPort())
		if err != nil {
			errChan <- fmt.Errorf("failed to resolve udp address: %v", err)
			return
		}
		if err = a.dnsMITM.ListenUDP(ctx, addr); err != nil {
			errChan <- fmt.Errorf("failed to serve DNS UDP proxy: %v", err)
		}
	}()

	go func() {
		addr, err := net.ResolveTCPAddr("tcp", a.Config().DNSProxy.Host.HostPort())
		if err != nil {
			errChan <- fmt.Errorf("failed to resolve tcp address: %v", err)
			return
		}
		if err = a.dnsMITM.ListenTCP(ctx, addr); err != nil {
			errChan <- fmt.Errorf("failed to serve DNS TCP proxy: %v", err)
		}
	}()
}

// dnsRequestHook обрабатывает входящие DNS-запросы
func (a *App) dnsRequestHook(clientAddr net.Addr, reqMsg dns.Msg, network string) (*dns.Msg, *dns.Msg, error) {
	var clientAddrStr string
	if clientAddr != nil {
		clientAddrStr = clientAddr.String()
	}
	idStr := formatID(reqMsg.Id)

	log.Debug().
		Str("id", idStr).
		Str("clientAddr", clientAddrStr).
		Str("network", network).
		Msg("request received")

	for _, q := range reqMsg.Question {
		log.Info().
			Str("id", idStr).
			Str("name", q.Name).
			Int("qtype", int(q.Qtype)).
			Int("qclass", int(q.Qclass)).
			Str("clientAddr", clientAddrStr).
			Str("network", network).
			Msg("requested record")
	}

	if a.Config().DNSProxy.DisableFakePTR {
		return nil, nil, nil
	}

	if len(reqMsg.Question) == 1 && reqMsg.Question[0].Qtype == dns.TypePTR {
		respMsg := &dns.Msg{
			MsgHdr: dns.MsgHdr{
				Id:                 reqMsg.Id,
				Response:           true,
				RecursionAvailable: true,
				Rcode:              dns.RcodeNameError,
			},
			Question: reqMsg.Question,
		}
		return nil, respMsg, nil
	}

	return nil, nil, nil
}

// dnsResponseHook installs routing before returning usable addresses to a client.
func (a *App) dnsResponseHook(clientAddr net.Addr, reqMsg dns.Msg, respMsg dns.Msg, network string) (*dns.Msg, error) {
	if err := a.handleMessage(respMsg, clientAddr, network); err != nil {
		log.Error().Err(err).Msg("DNS routing failed; withholding addresses")
		failure := new(dns.Msg)
		failure.SetRcode(&reqMsg, dns.RcodeServerFailure)
		return failure, nil
	}
	if a.Config().DNSProxy.DisableDropAAAA {
		return nil, nil
	}
	// HTTPS/SVCB ipv6hint can otherwise bypass AAAA filtering entirely.
	var answersChanged, nsChanged, extraChanged bool
	respMsg.Answer, answersChanged = withoutIPv6(respMsg.Answer)
	respMsg.Ns, nsChanged = withoutIPv6(respMsg.Ns)
	respMsg.Extra, extraChanged = withoutIPv6(respMsg.Extra)
	if !answersChanged && !nsChanged && !extraChanged {
		return nil, nil
	}
	respMsg.AuthenticatedData = false
	return &respMsg, nil
}

func withoutIPv6(records []dns.RR) ([]dns.RR, bool) {
	result := make([]dns.RR, 0, len(records))
	changed := false
	for _, rr := range records {
		if rr == nil || rr.Header().Rrtype == dns.TypeAAAA {
			changed = true
			continue
		}
		binding := serviceBinding(rr)
		hasIPv6 := false
		if binding != nil {
			for _, value := range binding.Value {
				if value.Key() == dns.SVCB_IPV6HINT {
					hasIPv6 = true
				}
			}
		}
		if !hasIPv6 {
			result = append(result, rr)
			continue
		}
		changed = true
		switch v := rr.(type) {
		case *dns.HTTPS:
			rr = dns.Copy(v)
			binding = &rr.(*dns.HTTPS).SVCB
		case *dns.SVCB:
			rr = dns.Copy(v)
			binding = rr.(*dns.SVCB)
		}
		if binding != nil {
			values := binding.Value[:0]
			valid := true
			for _, value := range binding.Value {
				if mandatory, ok := value.(*dns.SVCBMandatory); ok {
					for _, key := range mandatory.Code {
						if key == dns.SVCB_IPV6HINT {
							valid = false
						}
					}
				}
				if value.Key() != dns.SVCB_IPV6HINT {
					values = append(values, value)
				}
			}
			if !valid {
				continue
			}
			binding.Value = values
		}
		result = append(result, rr)
	}
	return result, changed
}

func serviceBinding(rr dns.RR) *dns.SVCB {
	switch v := rr.(type) {
	case *dns.HTTPS:
		return &v.SVCB
	case *dns.SVCB:
		return v
	}
	return nil
}

func (a *App) handleMessage(msg dns.Msg, clientAddr net.Addr, network string) error {
	if msg.Rcode != dns.RcodeSuccess {
		return nil
	}
	idStr := formatID(msg.Id)
	client := ""
	if clientAddr != nil {
		client = clientAddr.String()
	}
	records := append(append([]dns.RR(nil), msg.Answer...), msg.Extra...)
	// Learn the whole alias graph before any addresses, regardless of RR order.
	for _, rr := range records {
		switch v := rr.(type) {
		case *dns.CNAME:
			a.recordsCache.AddAlias(trimFQDN(v.Hdr.Name), trimFQDN(v.Target), routingTTL(v.Hdr.Ttl, a.Config().Netfilter.IPSet.AdditionalTTL))
		default:
			if binding := serviceBinding(rr); binding != nil {
				a.recordsCache.AddBinding(trimFQDN(binding.Hdr.Name), trimFQDN(binding.Target), routingTTL(binding.Hdr.Ttl, a.Config().Netfilter.IPSet.AdditionalTTL))
			}
		}
	}
	var errs []error
	for _, rr := range records {
		switch v := rr.(type) {
		case *dns.A:
			errs = append(errs, a.processARecord(*v, idStr, client, network))
		case *dns.AAAA:
			errs = append(errs, a.processAAAARecord(*v, idStr, client, network))
		case *dns.CNAME:
			errs = append(errs, a.routeCachedAddresses(trimFQDN(v.Hdr.Name)))
		default:
			if binding := serviceBinding(rr); binding != nil {
				for _, value := range binding.Value {
					switch hints := value.(type) {
					case *dns.SVCBIPv4Hint:
						for _, ip := range hints.Hint {
							errs = append(errs, a.processARecord(dns.A{Hdr: binding.Hdr, A: ip.To4()}, idStr, client, network))
						}
					case *dns.SVCBIPv6Hint:
						for _, ip := range hints.Hint {
							errs = append(errs, a.processAAAARecord(dns.AAAA{Hdr: binding.Hdr, AAAA: ip.To16()}, idStr, client, network))
						}
					}
				}
				errs = append(errs, a.routeCachedAddresses(trimFQDN(binding.Hdr.Name)))
			}
		}
	}
	return errors.Join(errs...)
}

func (a *App) processARecord(record dns.A, idStr, client, network string) error {
	ip := record.A.To4()
	if ip == nil {
		return fmt.Errorf("invalid A address for %s", record.Hdr.Name)
	}
	return a.processDNSAddress(record.Hdr, ip, idStr, client, network)
}

func (a *App) processAAAARecord(record dns.AAAA, idStr, client, network string) error {
	if len(record.AAAA) != net.IPv6len {
		return fmt.Errorf("invalid AAAA address for %s", record.Hdr.Name)
	}
	return a.processDNSAddress(record.Hdr, record.AAAA, idStr, client, network)
}

func (a *App) processDNSAddress(header dns.RR_Header, ip net.IP, idStr, client, network string) error {
	name := trimFQDN(header.Name)
	ttl := routingTTL(header.Ttl, a.Config().Netfilter.IPSet.AdditionalTTL)
	a.recordsCache.AddAddress(name, ip, ttl)
	log.Debug().Str("id", idStr).Str("name", name).Str("address", ip.String()).
		Str("clientAddr", client).Str("network", network).Msg("processing DNS address")
	return a.routeDNSAddress(a.recordsCache.GetAliases(name), ip, ttl)
}

func (a *App) processCNameRecord(record dns.CNAME, idStr, client, network string) error {
	name := trimFQDN(record.Hdr.Name)
	a.recordsCache.AddAlias(name, trimFQDN(record.Target), routingTTL(record.Hdr.Ttl, a.Config().Netfilter.IPSet.AdditionalTTL))
	return a.routeCachedAddresses(name)
}

func (a *App) routeCachedAddresses(name string) error {
	names := a.recordsCache.GetAliases(name)
	var errs []error
	now := time.Now()
	for _, address := range a.recordsCache.GetAddresses(name) {
		if ttl := remainingTTL(address.Deadline, now); ttl != 0 {
			errs = append(errs, a.routeDNSAddress(names, address.Address, ttl))
		}
	}
	return errors.Join(errs...)
}

func (a *App) routeDNSAddress(names []string, ip net.IP, ttl uint32) error {
	var errs []error
	for _, group := range a.dnsRuleSetSnapshot(names) {
		matched, err := group.addDNSAddress(names, ip, ttl)
		if err != nil {
			errs = append(errs, fmt.Errorf("group %s: %w", group.IDValue(), err))
		} else if matched {
			log.Info().Str("address", ip.String()).Str("group", group.DisplayName()).
				Str("groupId", group.IDValue().String()).Msg("added to routing")
		}
	}
	return errors.Join(errs...)
}

// Match and add under one lock: a concurrent group edit must not add a DNS
// address using a previous model's rules after the new model was published.
func (g *RuleSet) addDNSAddress(names []string, ip net.IP, ttl uint32) (bool, error) {
	g.locker.Lock()
	defer g.locker.Unlock()
	if !g.Enabled() || !g.ConfiguredEnabled() {
		return false, nil
	}
	for _, rule := range g.domainRules() {
		for _, name := range names {
			if rule.IsMatch(name) {
				if g.ipset == nil || g.ipsetToLink == nil {
					return true, fmt.Errorf("routing is not ready")
				}
				if len(ip) == net.IPv4len {
					return true, g.addIPv4Subnet(netfilterTools.IPv4Subnet{Address: [4]byte(ip), CIDR: 32}, &ttl)
				}
				return true, g.addIPv6Subnet(netfilterTools.IPv6Subnet{Address: [16]byte(ip), CIDR: 128}, &ttl)
			}
		}
	}
	return false, nil
}
