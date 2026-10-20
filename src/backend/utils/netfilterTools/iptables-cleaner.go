package netfilterTools

import (
	"errors"
	"fmt"
	"strings"

	"magitrickle/utils/iptables"
)

func (nh *Helper) cleanIPTables(ipt *iptables.IPTables) error {
	if ipt == nil {
		return nil
	}
	jumpToChainPrefix := "-j " + nh.ChainPrefix

	exists, err := ipt.GetCurrentRules()
	if err != nil {
		return fmt.Errorf("listing chains error: %w", err)
	}

	chainsToDelete := make(map[string][]string)
	for table, chains := range exists {

		for chain, rules := range chains {
			if strings.HasPrefix(chain, nh.ChainPrefix) {
				chainsToDelete[table] = append(chainsToDelete[table], chain)
				// Flush all owned chains before deleting any: routing tiers
				// reference other owned chains, in no guaranteed map order.
				if err := ipt.RegisterChainOverride(table, chain); err != nil {
					return err
				}
				continue
			}

			for _, r := range rules {
				if !r.Contains(jumpToChainPrefix) {
					continue
				}

				err = ipt.Delete(table, chain, r.Args()...)
				if errors.Is(err, iptables.ErrChainNotInitialized) {
					err = ipt.RegisterChainPatch(table, chain)
					if err != nil {
						return fmt.Errorf("chain register error: %w", err)
					}
					err = ipt.Delete(table, chain, r.Args()...)
				}
				if err != nil {
					return fmt.Errorf("rule deletion error: %w", err)
				}
			}
		}

	}
	if err := ipt.Commit(); err != nil {
		return fmt.Errorf("failed to unlink iptables chains: %w", err)
	}
	for table, chains := range chainsToDelete {
		for _, chain := range chains {
			err = ipt.RegisterChainDelete(table, chain)
			if err != nil {
				return fmt.Errorf("deleting chain error: %w", err)
			}
		}
	}

	err = ipt.Commit()
	if err != nil {
		return fmt.Errorf("failed to commit iptables rules: %w", err)
	}
	return nil
}

func (nh *Helper) CleanIPTables() error {
	// The ROUTE chain and its direct exemptions are removed with the rest.
	nh.directMu.Lock()
	nh.directSets = nil
	nh.directMu.Unlock()
	var errs []error
	errs = append(errs, nh.cleanIPTables(nh.IPTables4))
	errs = append(errs, nh.cleanIPTables(nh.IPTables6))
	return errors.Join(errs...)
}
