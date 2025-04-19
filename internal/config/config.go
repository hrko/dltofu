package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/hrko/dltofu/internal/hash" // 自身のモジュールパス
	"github.com/hrko/dltofu/internal/platform"
	"github.com/hrko/dltofu/internal/template"
	"gopkg.in/yaml.v3"
)

const CurrentVersion = "v1"

// Config は設定ファイル全体を表す構造体
type Config struct {
	Version       string             `yaml:"version"`
	HashAlgorithm string             `yaml:"hash_algorithm,omitempty"` // デフォルトは sha256
	Files         map[string]FileDef `yaml:"files"`                    // キーはファイル識別子
	path          string             // 設定ファイルのパス (相対パス解決用)
	logger        *slog.Logger
}

// FileDef はダウンロードするファイルごとの定義
type FileDef struct {
	URL             string                     `yaml:"url"` // テンプレート可
	Version         string                     `yaml:"version,omitempty"`
	Platforms       map[string]string          `yaml:"platforms,omitempty"`     // key: platform_id (linux), value: template_value (linux)
	Architectures   map[string]string          `yaml:"architectures,omitempty"` // key: arch_id (amd64), value: template_value (amd64, x86_64)
	Destination     string                     `yaml:"destination,omitempty"`   // ダウンロード/展開先 (相対/絶対パス)
	IsArchive       bool                       `yaml:"is_archive,omitempty"`
	StripComponents int                        `yaml:"strip_components,omitempty"`
	ExtractPaths    []string                   `yaml:"extract_paths,omitempty"`
	HashAlgorithm   string                     `yaml:"hash_algorithm,omitempty"` // ファイル固有設定
	Overrides       map[string]OverrideFileDef `yaml:"overrides,omitempty"`      // key: "platform/arch" (e.g., "linux/amd64")
}

// OverrideFileDef はプラットフォーム/アーキテクチャごとの上書き設定
type OverrideFileDef struct {
	URL           string   `yaml:"url,omitempty"`
	Destination   string   `yaml:"destination,omitempty"`
	HashAlgorithm string   `yaml:"hash_algorithm,omitempty"`
	ExtractPaths  []string `yaml:"extract_paths,omitempty"`
	// IsArchive や StripComponents は通常 Override しない想定だが、必要なら追加
}

// LoadConfig は指定されたパスから設定ファイルを読み込み、パースして検証する
func LoadConfig(configPath string, logger *slog.Logger) (*Config, error) {
	if logger == nil {
		logger = slog.Default() // フォールバック
	}

	if configPath == "" {
		return nil, fmt.Errorf("config file path is empty")
	}

	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path for config file %s: %w", configPath, err)
	}
	logger.Debug("Loading config file", "absolute_path", absPath)

	data, err := os.ReadFile(absPath)
	if err != nil {
		// 存在しない場合もこのエラー
		return nil, fmt.Errorf("failed to read config file %s: %w", absPath, err)
	}

	var cfg Config
	err = yaml.Unmarshal(data, &cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal config file %s: %w", absPath, err)
	}

	cfg.path = absPath // 読み込んだファイルの絶対パスを保持
	cfg.logger = logger

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config file validation failed: %w", err)
	}
	logger.Info("Config file loaded and validated successfully", "path", absPath)

	return &cfg, nil
}

// validate は読み込んだ設定の内容を検証する
func (c *Config) validate() error {
	if c.Version == "" {
		return fmt.Errorf("config version is missing")
	}
	if c.Version != CurrentVersion {
		return fmt.Errorf("unsupported config version: %s (supported: %s)", c.Version, CurrentVersion)
	}

	if c.HashAlgorithm == "" {
		c.HashAlgorithm = hash.AlgoSHA256 // デフォルト値設定
		c.logger.Debug("Global hash_algorithm not set, defaulting to sha256")
	} else if _, err := hash.GetHasher(c.HashAlgorithm); err != nil {
		return fmt.Errorf("invalid global hash_algorithm '%s': %w", c.HashAlgorithm, err)
	}

	if len(c.Files) == 0 {
		c.logger.Warn("No files defined in the configuration")
		// エラーにはしないが警告
	}

	for fileID, fileDef := range c.Files {
		if fileDef.URL == "" {
			return fmt.Errorf("file '%s': url is required", fileID)
		}
		if fileDef.HashAlgorithm != "" {
			if _, err := hash.GetHasher(fileDef.HashAlgorithm); err != nil {
				return fmt.Errorf("file '%s': invalid hash_algorithm '%s': %w", fileID, fileDef.HashAlgorithm, err)
			}
		}
		if fileDef.IsArchive && fileDef.StripComponents < 0 {
			return fmt.Errorf("file '%s': strip_components cannot be negative", fileID)
		}
		if !fileDef.IsArchive && (fileDef.StripComponents > 0 || len(fileDef.ExtractPaths) > 0) {
			c.logger.Warn("file '%s': strip_components and extract_paths are ignored when is_archive is false", "file_id", fileID)
		}

		// プラットフォーム/アーキテクチャ定義の検証
		if len(fileDef.Platforms) > 0 || len(fileDef.Architectures) > 0 {
			if len(fileDef.Platforms) == 0 {
				return fmt.Errorf("file '%s': architectures defined but platforms is missing", fileID)
			}
			if len(fileDef.Architectures) == 0 {
				return fmt.Errorf("file '%s': platforms defined but architectures is missing", fileID)
			}
			for pID := range fileDef.Platforms {
				if !platform.IsValidPlatform(pID) {
					return fmt.Errorf("file '%s': invalid platform identifier '%s'", fileID, pID)
				}
			}
			for aID := range fileDef.Architectures {
				if !platform.IsValidArch(aID) {
					return fmt.Errorf("file '%s': invalid architecture identifier '%s'", fileID, aID)
				}
			}
		} else {
			// プラットフォーム定義がないのに override があるのはおかしい
			if len(fileDef.Overrides) > 0 {
				return fmt.Errorf("file '%s': overrides are defined but platforms/architectures are not specified", fileID)
			}
		}

		// Override の検証
		for overrideKey, overrideDef := range fileDef.Overrides {
			parts := strings.SplitN(overrideKey, "/", 2)
			if len(parts) != 2 {
				return fmt.Errorf("file '%s': invalid override key format '%s', expected 'platform/arch'", fileID, overrideKey)
			}
			pID, aID := parts[0], parts[1]
			if _, ok := fileDef.Platforms[pID]; !ok {
				return fmt.Errorf("file '%s': override key '%s' contains platform '%s' not defined in platforms section", fileID, overrideKey, pID)
			}
			if _, ok := fileDef.Architectures[aID]; !ok {
				return fmt.Errorf("file '%s': override key '%s' contains architecture '%s' not defined in architectures section", fileID, overrideKey, aID)
			}
			if overrideDef.HashAlgorithm != "" {
				if _, err := hash.GetHasher(overrideDef.HashAlgorithm); err != nil {
					return fmt.Errorf("file '%s', override '%s': invalid hash_algorithm '%s': %w", fileID, overrideKey, overrideDef.HashAlgorithm, err)
				}
			}
			// 他のOverrideフィールドのバリデーションが必要なら追加
		}
	}

	return nil
}

// GetConfigDir は設定ファイルが存在するディレクトリのパスを返す
func (c *Config) GetConfigDir() string {
	return filepath.Dir(c.path)
}

// GetEffectiveHashAlgorithm はファイル定義とグローバル設定を考慮して、
// 特定のファイル (または Override) に適用されるハッシュアルゴリズムを返す
func (c *Config) GetEffectiveHashAlgorithm(fileID, platformID, archID string) string {
	fileDef, ok := c.Files[fileID]
	if !ok {
		// 通常は呼び出し元でチェックされるはず
		return c.HashAlgorithm // fallback to global
	}

	if platformID != "" && archID != "" {
		overrideKey := platformID + "/" + archID
		if overrideDef, ok := fileDef.Overrides[overrideKey]; ok {
			if overrideDef.HashAlgorithm != "" {
				return overrideDef.HashAlgorithm
			}
		}
	}

	if fileDef.HashAlgorithm != "" {
		return fileDef.HashAlgorithm
	}

	return c.HashAlgorithm // global default
}

// --- Helper functions to get effective values considering overrides ---

func (f *FileDef) GetEffectiveURL(platformValue, archValue, version string) (string, error) {
	// TODO: Implement logic considering overrides
	// この関数は template 処理と統合する方が良いかもしれない
	// ここでは単純化のため、Override は呼び出し側で先にチェックする想定
	data := template.TemplateData{
		Version:      version,
		Platform:     platformValue,
		Architecture: archValue,
	}
	return template.ResolveURL(f.URL, data)
}

// GetEffectiveDestination は Override を考慮した Destination を返す
func (f *FileDef) GetEffectiveDestination(platformID, archID string) string {
	if platformID != "" && archID != "" {
		overrideKey := platformID + "/" + archID
		if overrideDef, ok := f.Overrides[overrideKey]; ok && overrideDef.Destination != "" {
			return overrideDef.Destination
		}
	}
	return f.Destination
}

// GetEffectiveExtractPaths は Override を考慮した ExtractPaths を返す
func (f *FileDef) GetEffectiveExtractPaths(platformID, archID string) []string {
	if platformID != "" && archID != "" {
		overrideKey := platformID + "/" + archID
		if overrideDef, ok := f.Overrides[overrideKey]; ok && len(overrideDef.ExtractPaths) > 0 {
			return overrideDef.ExtractPaths
		}
	}
	return f.ExtractPaths
}

// ResolveDestPath は Destination を設定ファイルのパス基準で解決する
func (c *Config) ResolveDestPath(dest string) (string, error) {
	if dest == "" {
		// Destination が未指定の場合の挙動 (カレントディレクトリ？エラー？)
		// download コマンド側でURLからファイル名を推測してカレントに置くなど必要
		return "", fmt.Errorf("destination path is empty")
	}
	if filepath.IsAbs(dest) {
		return dest, nil
	}
	return filepath.Join(c.GetConfigDir(), dest), nil
}
