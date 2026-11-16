package httpheader

import "strings"

// MergeVary 将多个层的协商维度按首见顺序合并，并保留通配语义。
func MergeVary(groups ...[]string) string {
	seen := make(map[string]struct{})
	var result []string
	for _, values := range groups {
		for _, value := range values {
			for field := range strings.SplitSeq(value, ",") {
				field = strings.TrimSpace(field)
				if field == "*" {
					return "*"
				}
				if field == "" {
					continue
				}
				key := strings.ToLower(field)
				if _, exists := seen[key]; !exists {
					seen[key] = struct{}{}
					result = append(result, field)
				}
			}
		}
	}
	return strings.Join(result, ", ")
}
