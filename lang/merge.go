package lang

// MergeAll 严格解析目录内全部 JSON 文件，并把结果原子叠加到已有语言包。
// 它用于 ThinkPHP 多应用的“全局语言包后加载应用语言包”语义；同名应用翻译
// 覆盖全局翻译，目录解析失败时不修改任何既有状态。
func (l *Lang) MergeAll(directory string) error {
	if l == nil {
		return ErrInvalidLanguageFile
	}
	overlay := NewLang()
	if err := overlay.LoadAll(directory); err != nil {
		return err
	}
	overlay.lock.RLock()
	loaded := make(map[string]map[string]string, len(overlay.data))
	for language, translations := range overlay.data {
		loaded[language] = cloneTranslations(translations)
	}
	overlay.lock.RUnlock()
	if len(loaded) == 0 {
		return nil
	}

	l.lock.Lock()
	if l.data == nil {
		l.data = make(map[string]map[string]string)
	}
	for language, translations := range loaded {
		merged := cloneTranslations(l.data[language])
		for key, value := range translations {
			merged[key] = value
		}
		l.data[language] = merged
	}
	l.publishDetectionSnapshotLocked()
	l.lock.Unlock()
	return nil
}
