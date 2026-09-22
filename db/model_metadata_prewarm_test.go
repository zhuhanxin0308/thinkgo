package db

import (
	"errors"
	"reflect"
	"testing"
)

type prewarmModel struct {
	ID      int    `thinkgo:"id"`
	Display string `thinkgo:"display_name,omitempty"`
}

type invalidPrewarmModel struct {
	First  string `thinkgo:"same_column"`
	Second string `thinkgo:"same_column"`
}

// TestPrewarmModelMetadataValidatesDiscoveredModels 验证 schema:validate 对自动
// 发现模型执行真实字段预热，并在启动阶段拒绝非结构体和重复列映射。
func TestPrewarmModelMetadataValidatesDiscoveredModels(t *testing.T) {
	if err := PrewarmModelMetadata(reflect.TypeOf(prewarmModel{}), reflect.TypeOf(&prewarmModel{})); err != nil {
		t.Fatalf("预热合法模型元数据失败: %v", err)
	}
	metadata, err := cachedModelMetadata(reflect.TypeOf(prewarmModel{}))
	if err != nil || len(metadata.fields) != 2 {
		t.Fatalf("预热后的模型元数据错误: metadata=%#v err=%v", metadata, err)
	}

	for _, modelType := range []reflect.Type{nil, reflect.TypeOf("not-struct"), reflect.TypeOf(invalidPrewarmModel{})} {
		if err = PrewarmModelMetadata(modelType); !errors.Is(err, ErrInvalidModel) {
			t.Errorf("非法模型类型 %#v 必须返回 ErrInvalidModel: %v", modelType, err)
		}
	}
}
