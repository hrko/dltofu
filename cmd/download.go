package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/hrko/dltofu/internal/archive"
	"github.com/hrko/dltofu/internal/config"
	"github.com/hrko/dltofu/internal/download"
	"github.com/hrko/dltofu/internal/lock"
	"github.com/hrko/dltofu/internal/platform"
	"github.com/hrko/dltofu/internal/template"
	"github.com/spf13/cobra"
)

var forceDownload bool // --force フラグ用

// downloadCmd represents the download command
var downloadCmd = &cobra.Command{
	Use:   "download",
	Short: "Downloads files based on config and verifies against the lock file",
	Long: `Reads the configuration and lock file, determines the correct file variant
for the current platform/architecture, downloads it, and verifies its hash
against the lock file.

If the file is an archive, it extracts it according to the configuration
(strip_components, extract_paths). Use --force to overwrite existing files.`,
	RunE: runDownload,
}

func init() {
	rootCmd.AddCommand(downloadCmd)
	downloadCmd.Flags().BoolVarP(&forceDownload, "force", "f", false, "Overwrite existing files without asking")
}

func runDownload(cmd *cobra.Command, args []string) error {
	logger.Info("Starting download command", "force", forceDownload)

	if cfgFile == "" {
		return fmt.Errorf("configuration file must be specified using --config or exist as dltofu.yml/dltofu.yaml")
	}

	cfg, err := config.LoadConfig(cfgFile, logger)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Lock ファイルを読み込む (必須)
	configDir := cfg.GetConfigDir()
	lockFile, err := lock.LoadLockFile(configDir, logger)
	if err != nil {
		// download では lock ファイルは必須
		return fmt.Errorf("failed to load lock file (required for download): %w", err)
	}

	// 実行環境のプラットフォーム/アーキテクチャを取得
	currentPlatform, err := platform.GetCurrentPlatform()
	if err != nil {
		return fmt.Errorf("failed to get current platform: %w", err)
	}
	currentArch, err := platform.GetCurrentArch()
	if err != nil {
		return fmt.Errorf("failed to get current architecture: %w", err)
	}
	logger.Info("Detected execution environment", "platform", currentPlatform, "architecture", currentArch)

	// ダウンローダー準備
	downloader := download.NewDownloader(0, logger)

	// 設定ファイルの各ファイルを処理
	hasError := false // エラーが発生しても全ファイルの処理を試みるフラグ
	for fileID, fileDef := range cfg.Files {
		logger.Debug("Processing file definition", "file_id", fileID)

		targetPlatformID := ""
		targetArchID := ""
		platformValue := ""
		archValue := ""

		// この環境向けのファイルか判定
		if len(fileDef.Platforms) > 0 && len(fileDef.Architectures) > 0 {
			validPlatform := false
			if pVal, ok := fileDef.Platforms[currentPlatform]; ok {
				validPlatform = true
				targetPlatformID = currentPlatform
				platformValue = pVal
			}
			validArch := false
			if aVal, ok := fileDef.Architectures[currentArch]; ok {
				validArch = true
				targetArchID = currentArch
				archValue = aVal
			}

			if !validPlatform || !validArch {
				logger.Debug("Skipping file: not applicable for current platform/architecture", "file_id", fileID, "current_platform", currentPlatform, "current_arch", currentArch)
				continue // このファイルは現在の環境向けではない
			}
			logger.Debug("File applicable for current environment", "file_id", fileID, "platform", targetPlatformID, "arch", targetArchID)
		} else {
			// プラットフォーム指定がない場合は常にダウンロード対象
			logger.Debug("File does not have platform/architecture constraints", "file_id", fileID)
		}

		// URL 解決
		overrideKey := ""
		if targetPlatformID != "" && targetArchID != "" {
			overrideKey = targetPlatformID + "/" + targetArchID
		}

		urlTemplate := fileDef.URL
		if overrideDef, ok := fileDef.Overrides[overrideKey]; ok && overrideDef.URL != "" {
			urlTemplate = overrideDef.URL
		}
		tmplData := template.TemplateData{
			Version:      fileDef.Version,
			Platform:     platformValue,
			Architecture: archValue,
		}
		resolvedURL, err := template.ResolveURL(urlTemplate, tmplData)
		if err != nil {
			logger.Error("Failed to resolve URL template", "file_id", fileID, "error", err)
			hasError = true
			continue // 次のファイルへ
		}
		logger.Debug("Resolved URL for download", "file_id", fileID, "url", resolvedURL)

		// Lock ファイルから期待されるハッシュ値を取得
		expectedHash, found := lockFile.GetHash(fileID, resolvedURL)
		if !found {
			logger.Error("Hash not found in lock file for resolved URL", "file_id", fileID, "url", resolvedURL)
			hasError = true
			continue // 次のファイルへ
		}
		logger.Debug("Found expected hash in lock file", "file_id", fileID, "url", resolvedURL, "hash", expectedHash)

		// ダウンロード先パスを決定
		dest := fileDef.GetEffectiveDestination(targetPlatformID, targetArchID)
		if dest == "" {
			// Destination が未指定の場合、URLからファイル名を推測してカレントディレクトリに置く
			urlParts := strings.Split(resolvedURL, "/")
			dest = urlParts[len(urlParts)-1] // URLの最後の部分をファイル名とする
			logger.Debug("Destination not specified, using filename from URL", "file_id", fileID, "destination", dest)
			// この場合、設定ファイル基準ではなくカレントディレクトリ基準とする
			absDest, err := filepath.Abs(dest)
			if err != nil {
				logger.Error("Failed to get absolute path for default destination", "file_id", fileID, "destination", dest, "error", err)
				hasError = true
				continue
			}
			dest = absDest
		} else {
			absDest, err := cfg.ResolveDestPath(dest) // 設定ファイル基準で解決
			if err != nil {
				logger.Error("Failed to resolve destination path", "file_id", fileID, "destination", dest, "error", err)
				hasError = true
				continue
			}
			dest = absDest
		}
		logger.Debug("Resolved final destination path", "file_id", fileID, "path", dest)

		// 既存ファイルのチェック (非アーカイブの場合のみ事前チェック)
		if !fileDef.IsArchive {
			if _, err := os.Stat(dest); err == nil {
				// ファイルが存在する
				if !forceDownload {
					// TODO: インタラクティブな確認を実装する場合はここ
					logger.Warn("Destination file already exists. Skipping download.", "file_id", fileID, "path", dest, "hint", "Use --force to overwrite.")
					continue // スキップ
				} else {
					logger.Debug("Destination file exists, proceeding with overwrite (--force)", "file_id", fileID, "path", dest)
					// 上書き実行
				}
			} else if !os.IsNotExist(err) {
				// Stat で予期せぬエラー
				logger.Error("Failed to check destination file", "file_id", fileID, "path", dest, "error", err)
				hasError = true
				continue
			}
			// ファイルが存在しない場合はそのまま進む
		} else {
			// アーカイブの場合、展開先ディレクトリが存在するかどうかだけ確認・作成
			// 個々のファイルの上書きは展開処理内で行う
			if err := os.MkdirAll(dest, 0755); err != nil { // dest はディレクトリパスのはず
				logger.Error("Failed to create destination directory for archive", "file_id", fileID, "path", dest, "error", err)
				hasError = true
				continue
			}
			logger.Debug("Ensured destination directory exists for archive", "file_id", fileID, "path", dest)
		}

		// ダウンロード実行 (ハッシュ検証含む)
		// アーカイブの場合、一時ファイルにダウンロードしてから展開する
		var downloadedFilePath string
		if fileDef.IsArchive {
			// 一時ファイルにダウンロード
			tempArchiveFile, err := os.CreateTemp("", fmt.Sprintf("dltofu-%s-*.tmp", fileID))
			if err != nil {
				logger.Error("Failed to create temporary file for archive download", "file_id", fileID, "error", err)
				hasError = true
				continue
			}
			downloadedFilePath = tempArchiveFile.Name()
			tempArchiveFile.Close()             // downloader が再度開くので一旦閉じる
			defer os.Remove(downloadedFilePath) // 展開後またはエラー時に削除

			logger.Debug("Downloading archive to temporary file", "file_id", fileID, "url", resolvedURL, "temp_path", downloadedFilePath)
			err = downloader.FetchToFile(resolvedURL, downloadedFilePath, expectedHash)
		} else {
			// 通常ファイルは直接ダウンロード先に保存 (FetchToFile内で上書き処理も行う)
			downloadedFilePath = dest
			logger.Debug("Downloading file directly", "file_id", fileID, "url", resolvedURL, "destination", downloadedFilePath)
			err = downloader.FetchToFile(resolvedURL, downloadedFilePath, expectedHash)
		}

		if err != nil {
			logger.Error("Download or hash verification failed", "file_id", fileID, "url", resolvedURL, "error", err)
			// FetchToFile 内で中途半端なファイルは削除されるはず
			hasError = true
			continue
		}
		logger.Info("Download and hash verification successful", "file_id", fileID, "url", resolvedURL)

		// アーカイブ展開処理
		if fileDef.IsArchive {
			logger.Info("Starting archive extraction", "file_id", fileID, "source", downloadedFilePath, "destination", dest)
			extractor, err := archive.GetExtractor(downloadedFilePath) // 一時ファイル名で判定
			if err != nil {
				logger.Error("Failed to get extractor for archive", "file_id", fileID, "path", downloadedFilePath, "error", err)
				hasError = true
				continue
			}

			extractPaths := fileDef.GetEffectiveExtractPaths(targetPlatformID, targetArchID)

			err = extractor.Extract(downloadedFilePath, dest, fileDef.StripComponents, extractPaths, forceDownload, logger)
			if err != nil {
				logger.Error("Archive extraction failed", "file_id", fileID, "source", downloadedFilePath, "error", err)
				// 展開に失敗した場合、部分的に展開されたファイルが残る可能性がある
				hasError = true
				continue
			}
			logger.Info("Archive extraction successful", "file_id", fileID, "destination", dest)
			// 一時アーカイブファイルは defer で削除される
		} else {
			// 非アーカイブの場合、必要なら実行権限を付与
			// TODO: 設定ファイルでパーミッションを指定できるようにする？
			// とりあえず、基本的な実行権限を試みる (Unix系のみ)
			if runtime.GOOS != "windows" {
				if err := os.Chmod(downloadedFilePath, 0755); err != nil {
					// エラーにはしないが警告
					logger.Warn("Failed to set executable permission", "path", downloadedFilePath, "error", err)
				} else {
					logger.Debug("Set executable permission", "path", downloadedFilePath)
				}
			}
		}
		logger.Info("Successfully processed file", "file_id", fileID)

	} // end file loop

	if hasError {
		return fmt.Errorf("download command finished with errors")
	}

	logger.Info("Download command finished successfully")
	return nil
}
