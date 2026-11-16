package contract

import "strings"

// OracleExplicitKeyDestination 从明确的主键输入选择回传绑定。Oracle 将空字符串视为 NULL。
func OracleExplicitKeyDestination(data map[string]any, primary string) any {
	primary = strings.ToUpper(primary)
	for name, value := range data {
		if strings.ToUpper(name) != primary {
			continue
		}
		switch value := value.(type) {
		case string:
			if value != "" {
				return new(string)
			}
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
			return new(int64)
		}
		break
	}
	return nil
}
