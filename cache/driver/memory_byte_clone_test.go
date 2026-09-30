package driver

import (
	"reflect"
	"sync"
	"testing"
)

type cloneTestByte uint8
type cloneTestBytes []byte

func TestMemoryByteClonePreservesTypesAndOwnership(t *testing.T) {
	inputs := []interface{}{
		[]byte{1, 2}, cloneTestBytes{1, 2}, []cloneTestByte{1, 2},
		[]byte(nil), cloneTestBytes(nil), []cloneTestByte(nil),
		[]byte{}, cloneTestBytes{}, []cloneTestByte{},
		make([]byte, 2, 8),
	}
	for _, input := range inputs {
		t.Run(reflect.TypeOf(input).String(), func(t *testing.T) {
			got := cloneMemoryValue(input)
			if !reflect.DeepEqual(got, input) {
				t.Fatalf("clone changed type or contents: got=%#v input=%#v", got, input)
			}
			source, cloned := reflect.ValueOf(input), reflect.ValueOf(got)
			if source.IsNil() != cloned.IsNil() || cloned.Cap() != source.Len() {
				t.Fatal("clone changed nil/empty or exact-capacity semantics")
			}
			if source.Len() == 0 {
				return
			}
			original := source.Index(0).Uint()
			source.Index(0).SetUint(7)
			if cloned.Index(0).Uint() != original {
				t.Fatal("clone shares the caller's byte storage")
			}
			cloned.Index(0).SetUint(9)
			if source.Index(0).Uint() != 7 {
				t.Fatal("mutating the clone changed its source")
			}
		})
	}
}

func TestMemoryNestedByteSnapshotsStayIndependent(t *testing.T) {
	driver := NewMemory()
	payload := []byte{1, 2, 3}
	original := map[string]interface{}{"a": payload, "b": payload}
	original["self"] = original
	if err := driver.Set("nested", original, 0); err != nil {
		t.Fatal(err)
	}
	payload[0] = 8
	var wait sync.WaitGroup
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for j := 0; j < 50; j++ {
				value, found, err := driver.Get("nested")
				if err != nil || !found {
					t.Error("snapshot lookup failed")
					return
				}
				snapshot := value.(map[string]interface{})
				a, b := snapshot["a"].([]byte), snapshot["b"].([]byte)
				if a[0] != 1 || b[0] != 1 {
					t.Error("snapshot shares source or concurrent reader storage")
					return
				}
				a[0] = 9
				if b[0] != 9 {
					t.Error("clone lost alias identity within the private graph")
				}
				snapshot["self"].(map[string]interface{})["extra"] = true
				if snapshot["extra"] != true {
					t.Error("clone did not preserve the private cycle")
				}
			}
		}()
	}
	wait.Wait()
}
