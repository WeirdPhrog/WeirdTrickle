package recordsCache

import (
	"bytes"
	"context"
	"net"
	"strings"
	"sync"
	"time"
)

type Address struct {
	Address  net.IP
	Deadline time.Time
}

type Alias struct {
	Alias    string
	Deadline time.Time
}

type Records struct {
	locker sync.RWMutex

	// Раздельные map'ы вместо map[string]interface{}
	addresses map[string][]*Address
	aliases   map[string]*Alias
	// HTTPS/SVCB may advertise several targets for the same owner.
	bindings        map[string]map[string]time.Time
	reverseBindings map[string]map[string]time.Time

	// Обратный индекс: alias → []domains, которые на него ссылаются
	reverseAliases map[string][]string
}

func (r *Records) AddAlias(domainName, alias string, ttl uint32) {
	domainName, alias = strings.ToLower(domainName), strings.ToLower(alias)
	if domainName == alias {
		return
	}

	r.locker.Lock()
	defer r.locker.Unlock()

	deadline := time.Now().Add(time.Duration(ttl) * time.Second)

	// Удаляем старый reverse alias если был
	if oldAlias, ok := r.aliases[domainName]; ok {
		if oldAlias.Alias == alias && oldAlias.Deadline.After(deadline) {
			deadline = oldAlias.Deadline
		}
		r.removeReverseAlias(oldAlias.Alias, domainName)
	}

	r.aliases[domainName] = &Alias{
		Alias:    alias,
		Deadline: deadline,
	}

	// Добавляем reverse alias
	r.reverseAliases[alias] = append(r.reverseAliases[alias], domainName)
}

func (r *Records) removeReverseAlias(alias, domainName string) {
	domains := r.reverseAliases[alias]
	for i, d := range domains {
		if d == domainName {
			// Удаляем элемент без сохранения порядка
			domains[i] = domains[len(domains)-1]
			r.reverseAliases[alias] = domains[:len(domains)-1]
			break
		}
	}
	if len(r.reverseAliases[alias]) == 0 {
		delete(r.reverseAliases, alias)
	}
}

func (r *Records) AddAddress(domainName string, addr net.IP, ttl uint32) {
	domainName = strings.ToLower(domainName)
	r.locker.Lock()
	defer r.locker.Unlock()

	deadline := time.Now().Add(time.Duration(ttl) * time.Second)

	addresses := r.addresses[domainName]
	for _, aRecord := range addresses {
		// Compare bytes, not net.IP.Equal: a 4-byte A address and a 16-byte
		// IPv4-mapped AAAA address are routed through different ipsets.
		if bytes.Equal(aRecord.Address, addr) {
			// Another client may still use an earlier answer with a longer TTL.
			if deadline.After(aRecord.Deadline) {
				aRecord.Deadline = deadline
			}
			return
		}
	}

	r.addresses[domainName] = append(addresses, &Address{
		Address:  bytes.Clone(addr),
		Deadline: deadline,
	})
}

func (r *Records) AddBinding(owner, target string, ttl uint32) {
	owner, target = strings.ToLower(owner), strings.ToLower(target)
	if owner == target || target == "" || target == "." {
		return
	}
	r.locker.Lock()
	defer r.locker.Unlock()
	if r.bindings[owner] == nil {
		r.bindings[owner] = make(map[string]time.Time)
	}
	deadline := time.Now().Add(time.Duration(ttl) * time.Second)
	if deadline.After(r.bindings[owner][target]) {
		r.bindings[owner][target] = deadline
		if r.reverseBindings[target] == nil {
			r.reverseBindings[target] = make(map[string]time.Time)
		}
		r.reverseBindings[target][owner] = deadline
	}
}

// GetAliases возвращает все домены, которые ссылаются на данный (прямо или транзитивно)
func (r *Records) GetAliases(domainName string) []string {
	domainName = strings.ToLower(domainName)
	r.locker.RLock()
	defer r.locker.RUnlock()

	result := []string{domainName}
	queue := []string{domainName}
	seen := make(map[string]struct{})
	seen[domainName] = struct{}{}
	now := time.Now()

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, pointing := range r.reverseAliases[current] {
			if alias := r.aliases[pointing]; alias == nil || !now.Before(alias.Deadline) {
				continue
			}
			if _, ok := seen[pointing]; ok {
				continue
			}
			seen[pointing] = struct{}{}
			result = append(result, pointing)
			queue = append(queue, pointing)
		}
		for owner, deadline := range r.reverseBindings[current] {
			if !now.Before(deadline) {
				continue
			}
			if _, ok := seen[owner]; !ok {
				seen[owner] = struct{}{}
				result = append(result, owner)
				queue = append(queue, owner)
			}
		}
	}

	return result
}

func (r *Records) GetAddresses(domainName string) []*Address {
	domainName = strings.ToLower(domainName)
	r.locker.RLock()
	defer r.locker.RUnlock()

	now := time.Now()
	seen := make(map[string]struct{})
	seen[domainName] = struct{}{}

	queue := []string{domainName}
	var result []*Address
	for len(queue) > 0 {
		domainName, queue = queue[0], queue[1:]
		for target, deadline := range r.bindings[domainName] {
			if _, ok := seen[target]; !ok && now.Before(deadline) {
				seen[target] = struct{}{}
				queue = append(queue, target)
			}
		}
		if addresses, ok := r.addresses[domainName]; ok && len(addresses) > 0 {
			// Фильтруем просроченные адреса (только чтение, без удаления)
			var valid []*Address
			for _, addr := range addresses {
				if !now.After(addr.Deadline) {
					// Readers retain these records after the lock is released.
					valid = append(valid, &Address{Address: bytes.Clone(addr.Address), Deadline: addr.Deadline})
				}
			}
			if len(valid) > 0 {
				result = append(result, valid...)
				continue
			}
		}

		alias, ok := r.aliases[domainName]
		if !ok || now.After(alias.Deadline) {
			continue
		}

		// Защита от циклов
		if _, ok := seen[alias.Alias]; ok {
			continue
		}
		seen[alias.Alias] = struct{}{}
		queue = append(queue, alias.Alias)
	}
	return result
}

func (r *Records) ListKnownDomains() []string {
	r.locker.RLock()
	defer r.locker.RUnlock()

	// Собираем уникальные домены из обоих map'ов
	domains := make(map[string]struct{}, len(r.addresses)+len(r.aliases))

	for name := range r.addresses {
		domains[name] = struct{}{}
	}

	for name := range r.aliases {
		domains[name] = struct{}{}
	}
	for name := range r.bindings {
		domains[name] = struct{}{}
	}

	result := make([]string, 0, len(domains))
	for name := range domains {
		result = append(result, name)
	}
	return result
}

// cleanupRecords удаляет истёкшие записи
func (r *Records) cleanupRecords() {
	r.locker.Lock()
	defer r.locker.Unlock()

	now := time.Now()

	// Очистка адресов
	for name, addresses := range r.addresses {
		idx := 0
		for _, addr := range addresses {
			if !now.After(addr.Deadline) {
				addresses[idx] = addr
				idx++
			}
		}
		if idx == 0 {
			delete(r.addresses, name)
		} else {
			r.addresses[name] = addresses[:idx]
		}
	}

	// Очистка алиасов
	for name, alias := range r.aliases {
		if now.After(alias.Deadline) {
			r.removeReverseAlias(alias.Alias, name)
			delete(r.aliases, name)
		}
	}
	for owner, targets := range r.bindings {
		for target, deadline := range targets {
			if !now.Before(deadline) {
				delete(targets, target)
				delete(r.reverseBindings[target], owner)
				if len(r.reverseBindings[target]) == 0 {
					delete(r.reverseBindings, target)
				}
			}
		}
		if len(targets) == 0 {
			delete(r.bindings, owner)
		}
	}
}

// StartCleanup запускает фоновую очистку с заданным интервалом
func (r *Records) StartCleanup(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				r.cleanupRecords()
			case <-ctx.Done():
				return
			}
		}
	}()
}

func New() *Records {
	return &Records{
		addresses:       make(map[string][]*Address),
		aliases:         make(map[string]*Alias),
		bindings:        make(map[string]map[string]time.Time),
		reverseBindings: make(map[string]map[string]time.Time),
		reverseAliases:  make(map[string][]string),
	}
}
