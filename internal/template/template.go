package template

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/hrko/dltofu/internal/model"
)

// TemplateData はURLテンプレートに渡されるデータ
type TemplateData struct {
	Version      string
	Platform     string // 置換後のプラットフォーム文字列 (e.g., linux, darwin, windows)
	Architecture string // 置換後のアーキテクチャ文字列 (e.g., amd64, arm64, x86_64)
}

// ResolveURL はテンプレート文字列とデータを使ってURLを生成する
func ResolveURL(urlTemplate string, data TemplateData) (model.ResolvedURL, error) {
	tmpl, err := template.New("url").Parse(urlTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse URL template: %w", err)
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, data)
	if err != nil {
		// 未定義の変数を参照した場合などにエラーになる
		return "", fmt.Errorf("failed to execute URL template: %w", err)
	}

	return model.ResolvedURL(buf.String()), nil
}
