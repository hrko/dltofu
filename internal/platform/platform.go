package platform

import (
	"fmt"
	"runtime"
)

// マッピング定義
var goosMap = map[string]string{
	"darwin":  "macos",
	"linux":   "linux",
	"windows": "windows",
}

var goarchMap = map[string]string{
	"amd64": "amd64",
	"arm64": "arm64",
}

// GetCurrentPlatform は実行環境のプラットフォーム識別子を返す
func GetCurrentPlatform() (string, error) {
	os := runtime.GOOS
	if p, ok := goosMap[os]; ok {
		return p, nil
	}
	return "", fmt.Errorf("unsupported GOOS: %s", os)
}

// GetCurrentArch は実行環境のアーキテクチャ識別子を返す
func GetCurrentArch() (string, error) {
	arch := runtime.GOARCH
	if a, ok := goarchMap[arch]; ok {
		return a, nil
	}
	return "", fmt.Errorf("unsupported GOARCH: %s", arch)
}

// IsValidPlatform は指定された識別子がサポートされているか返す
func IsValidPlatform(p string) bool {
	if _, ok := goosMap[p]; !ok {
		return false
	}
	for _, v := range goosMap {
		if v == p {
			return true
		}
	}
	return false
}

// IsValidArch は指定された識別子がサポートされているか返す
func IsValidArch(a string) bool {
	if _, ok := goarchMap[a]; !ok {
		return false
	}
	for _, v := range goarchMap {
		if v == a {
			return true
		}
	}
	return false
}

// GetAllPlatforms はサポートするプラットフォーム識別子のリストを返す
func GetAllPlatforms() []string {
	platforms := make([]string, 0, len(goosMap))
	for k := range goosMap {
		platforms = append(platforms, goosMap[k]) // 値を返す
	}
	return platforms
}

// GetAllArchs はサポートするアーキテクチャ識別子のリストを返す
func GetAllArchs() []string {
	archs := make([]string, 0, len(goarchMap))
	for k := range goarchMap {
		archs = append(archs, goarchMap[k]) // 値を返す
	}
	return archs
}

// GetGoos はプラットフォーム識別子から runtime.GOOS 文字列を取得する (主にテスト用や内部変換用)
func GetGoos(platformID string) (string, bool) {
	for k, v := range goosMap {
		if v == platformID {
			return k, true
		}
	}
	return "", false
}

// GetGoarch はアーキテクチャ識別子から runtime.GOARCH 文字列を取得する (主にテスト用や内部変換用)
func GetGoarch(archID string) (string, bool) {
	for k, v := range goarchMap {
		if v == archID {
			return k, true
		}
	}
	return "", false
}
