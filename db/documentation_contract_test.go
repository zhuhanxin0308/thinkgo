package db

import "testing"

type documentationUser struct {
	ID   int64  `thinkgo:"id,omitempty"`
	Name string `thinkgo:"name"`
}

func TestDocumentedThinkPHPWriteAPICompiles(t *testing.T) {
	var query *Query
	var model *Model
	_ = func() error {
		_, _ = query.Insert(map[string]interface{}{"name": "Ada"})
		_, _ = query.InsertGetId(map[string]interface{}{"name": "Ada"})
		_, _ = query.InsertAll([]map[string]interface{}{{"name": "Ada"}})
		_, _ = query.Save(map[string]interface{}{"name": "Ada"})
		_, _ = query.UpdateResult(map[string]interface{}{"name": "Ada"})
		_, _ = query.DeleteResult()
		_, _ = query.DetachDeleteResult()
		if err := model.Create(&documentationUser{}); err != nil {
			return err
		}
		return model.Save()
	}
}
