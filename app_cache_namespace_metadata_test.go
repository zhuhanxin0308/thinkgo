package framework

import "testing"

func TestAppMemoryTagFlushSurvivesCapacityPressure(t *testing.T) {
	for _, test := range []struct {
		name      string
		prefix    string
		tagPrefix string
	}{
		{name: "defaults"},
		{name: "configured-unicode-prefixes", prefix: "租户:", tagPrefix: "标签:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := map[string]interface{}{"type": "memory", "max_entries": 6}
			if test.prefix != "" {
				store["prefix"] = test.prefix
				store["tag_prefix"] = test.tagPrefix
			}
			manager, err := createAppCache(&App{BasePath: t.TempDir()}, map[string]interface{}{
				"default": "memory",
				"stores":  map[string]interface{}{"memory": store},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := manager.Close(); err != nil {
					t.Errorf("close configured cache: %v", err)
				}
			})
			tagged, err := manager.Tag("catalog")
			if err != nil {
				t.Fatal(err)
			}
			if err := tagged.Set("old", "old-value", 0); err != nil {
				t.Fatal(err)
			}
			if err := manager.Set("victim", "ordinary-value", 0); err != nil {
				t.Fatal(err)
			}
			if err := tagged.Set("live", "live-value", 0); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"pressure-one", "pressure-two"} {
				if err := manager.Set(key, key, 0); err != nil {
					t.Fatal(err)
				}
			}
			if value, found, err := manager.Get("live"); err != nil || !found || value != "live-value" {
				t.Fatalf("configured cache lost the live tagged member: value=%v found=%t err=%v", value, found, err)
			}
			if err := tagged.Flush(); err != nil {
				t.Fatalf("configured cache tag Flush failed: %v", err)
			}
			if value, found, err := manager.Get("live"); err != nil || found {
				t.Fatalf("configured cache tag Flush left its live member: value=%v found=%t err=%v", value, found, err)
			}
			for _, key := range []string{"pressure-one", "pressure-two"} {
				if value, found, err := manager.Get(key); err != nil || !found || value != key {
					t.Fatalf("configured tag Flush changed unrelated data: key=%q value=%v found=%t err=%v", key, value, found, err)
				}
			}
		})
	}
}
