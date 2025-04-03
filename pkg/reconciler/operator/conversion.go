package operator

import (
	"fmt"
	"net"
	"sort"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
)

func (crr *ConfigRevisionReconciler) buildFilterItems(items []v1alpha1.VrfRouteConfigurationPrefixItem, family AddressFamily) ([]v1alpha1.FilterItem, error) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Seq < items[j].Seq
	})

	var filterItems []v1alpha1.FilterItem
	for _, item := range items {
		if ip, _, err := net.ParseCIDR(item.CIDR); err != nil {
			return nil, fmt.Errorf("failed to parse CIDR %s: %w", item.CIDR, err)
		} else {
			if family == IPv4 && ip.To4() == nil {
				continue
			}
			if family == IPv6 && ip.To4() != nil {
				continue
			}
		}

		filterItem := v1alpha1.FilterItem{
			Matcher: v1alpha1.Matcher{
				Prefix: &v1alpha1.PrefixMatcher{
					Prefix: item.CIDR,
					Ge:     item.GE,
					Le:     item.LE,
				},
			},
		}
		filterItem.Action = v1alpha1.Action{
			Type: v1alpha1.Reject,
		}
		if item.Action == "permit" {
			filterItem.Action.Type = v1alpha1.Accept
		}
		filterItems = append(filterItems, filterItem)
	}
	return filterItems, nil
}
