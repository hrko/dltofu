package cmd

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	"github.com/hrko/dltofu/internal/config"
	"github.com/hrko/dltofu/internal/download"
	"github.com/hrko/dltofu/internal/lock"
	"github.com/hrko/dltofu/internal/model"
	"github.com/hrko/dltofu/internal/template"
)

// lockCmd represents the lock command
var lockCmd = &cobra.Command{
	Use:   "lock",
	Short: "Downloads files (temporarily) and generates/updates the lock file",
	Long: `Reads the configuration file, downloads all specified file variants
(all platforms/architectures if defined), calculates their hashes,
and writes them to the lock file (dltofu.lock).

It checks for hash inconsistencies with the existing lock file (if any)
and prunes entries that are no longer in the configuration.`,
	RunE: runLock,
}

func init() {
	rootCmd.AddCommand(lockCmd)
	// lock コマンド固有のフラグがあればここに追加
	// 例: lockCmd.Flags().IntP("parallelism", "p", runtime.NumCPU(), "Number of parallel downloads/hash calculations")
}

func runLock(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context() // Cobra v1.8+

	logger.Info("Starting lock command")

	if cfgFile == "" {
		// PersistentPreRun でデフォルトを探した後でも空ならエラー
		return fmt.Errorf("configuration file must be specified using --config or exist as dltofu.yml/dltofu.yaml")
	}

	cfg, err := config.LoadConfig(cfgFile, logger)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// 既存の Lock ファイルを読み込む (存在しなくてもエラーにはしない)
	configDir := cfg.GetConfigDir()
	existingLock, err := lock.LoadLockFile(configDir, logger)
	if err != nil && !errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "lock file not found") {
		// 読み込み自体に失敗した場合 (JSON不正など) はエラー
		logger.Error("Failed to load existing lock file, proceeding without it", "error", err)
		existingLock = lock.NewLockFile(logger) // 空のLockファイルとして扱う
	} else if existingLock == nil {
		existingLock = lock.NewLockFile(logger) // 新規作成
	}

	// 新しいLockファイルデータを準備
	newLock := lock.NewLockFile(logger)

	// ダウンローダー準備
	downloader := download.NewDownloader(0, logger) // Timeout はデフォルト

	// 並列処理の準備
	// parallelism, _ := cmd.Flags().GetInt("parallelism") // フラグから取得する場合
	parallelism := runtime.NumCPU() // CPU数で制限
	logger.Debug("Using parallelism", "count", parallelism)
	sem := semaphore.NewWeighted(int64(parallelism))
	g, ctx := errgroup.WithContext(ctx) // エラーが発生したら他のゴルーチンもキャンセル

	// アクティブなファイルとURLのセット (Prune用)
	activeFiles := make(map[lock.FileID]map[lock.ResolvedURL]struct{})
	var activeFilesMu sync.Mutex // activeFiles へのアクセス保護

	// 設定ファイルの各ファイルを処理
	for fileID, fileDef := range cfg.Files {
		// ループ変数をキャプチャ
		fileID := fileID
		fileDef := fileDef

		if len(fileDef.Platforms) > 0 && len(fileDef.Architectures) > 0 {
			// プラットフォーム/アーキテクチャ指定がある場合
			for pID, pVal := range fileDef.Platforms {
				for aID, aVal := range fileDef.Architectures {
					// ループ変数をキャプチャ
					pID := pID
					pVal := pVal
					aID := aID
					aVal := aVal

					g.Go(func() error {
						if err := sem.Acquire(ctx, 1); err != nil {
							return err // Context cancelled or semaphore closed
						}
						defer sem.Release(1)

						// URL 解決
						overrideKey := pID + "/" + aID
						urlTemplate := fileDef.URL
						if overrideDef, ok := fileDef.Overrides[overrideKey]; ok && overrideDef.URL != "" {
							urlTemplate = overrideDef.URL
						}
						tmplData := template.TemplateData{
							Version:      fileDef.Version,
							Platform:     pVal,
							Architecture: aVal,
						}
						resolvedURL, err := template.ResolveURL(urlTemplate, tmplData)
						if err != nil {
							logger.Error("Failed to resolve URL template", "file_id", fileID, "platform", pID, "arch", aID, "error", err)
							return fmt.Errorf("failed to resolve URL for %s (%s/%s): %w", fileID, pID, aID, err) // エラーを返し、errgroup を停止
						}
						logger.Debug("Resolved URL", "file_id", fileID, "platform", pID, "arch", aID, "url", resolvedURL)

						// アクティブな URL として記録
						activeFilesMu.Lock()
						if _, ok := activeFiles[fileID]; !ok {
							activeFiles[fileID] = make(map[model.ResolvedURL]struct{})
						}
						activeFiles[fileID][resolvedURL] = struct{}{}
						activeFilesMu.Unlock()

						// ダウンロードしてハッシュ計算
						hashAlgo := cfg.GetEffectiveHashAlgorithm(fileID, pID, aID)
						hash, err := downloader.Hash(resolvedURL, hashAlgo)
						if err != nil {
							logger.Error("Failed to download or hash", "file_id", fileID, "platform", pID, "arch", aID, "url", resolvedURL, "error", err)
							// ダウンロード失敗は lock コマンドではエラーにする (URLが間違っている可能性)
							return fmt.Errorf("failed download/hash for %s (%s/%s) URL %s: %w", fileID, pID, aID, resolvedURL, err)
						}

						// 新しい Lock データに設定 (既存チェック含む)
						// SetHash はスレッドセーフにする必要がある
						err = newLock.SetHash(fileID, resolvedURL, hash)
						if err != nil {
							logger.Error("Hash inconsistency detected", "file_id", fileID, "platform", pID, "arch", aID, "url", resolvedURL, "error", err)
							// ハッシュ不整合は致命的エラー
							return fmt.Errorf("hash inconsistency for %s (%s/%s) URL %s: %w", fileID, pID, aID, resolvedURL, err)
						}
						logger.Info("Processed", "file_id", fileID, "platform", pID, "arch", aID, "url", resolvedURL, "hash", hash)

						return nil
					})
				}
			}
		} else {
			// プラットフォーム/アーキテクチャ指定がない場合
			g.Go(func() error {
				if err := sem.Acquire(ctx, 1); err != nil {
					return err
				}
				defer sem.Release(1)

				// URL 解決 (バージョンのみ)
				tmplData := template.TemplateData{Version: fileDef.Version}
				resolvedURL, err := template.ResolveURL(fileDef.URL, tmplData)
				if err != nil {
					logger.Error("Failed to resolve URL template", "file_id", fileID, "error", err)
					return fmt.Errorf("failed to resolve URL for %s: %w", fileID, err)
				}
				logger.Debug("Resolved URL", "file_id", fileID, "url", resolvedURL)

				// アクティブな URL として記録
				activeFilesMu.Lock()
				if _, ok := activeFiles[fileID]; !ok {
					activeFiles[fileID] = make(map[model.ResolvedURL]struct{})
				}
				activeFiles[fileID][resolvedURL] = struct{}{}
				activeFilesMu.Unlock()

				// ダウンロードしてハッシュ計算
				hashAlgo := cfg.GetEffectiveHashAlgorithm(fileID, "", "")
				hash, err := downloader.Hash(resolvedURL, hashAlgo)
				if err != nil {
					logger.Error("Failed to download or hash", "file_id", fileID, "url", resolvedURL, "error", err)
					return fmt.Errorf("failed download/hash for %s URL %s: %w", fileID, resolvedURL, err)
				}

				// 新しい Lock データに設定
				err = newLock.SetHash(fileID, resolvedURL, hash)
				if err != nil {
					logger.Error("Hash inconsistency detected", "file_id", fileID, "url", resolvedURL, "error", err)
					return fmt.Errorf("hash inconsistency for %s URL %s: %w", fileID, resolvedURL, err)
				}
				logger.Info("Processed", "file_id", fileID, "url", resolvedURL, "hash", hash)

				return nil
			})
		}
	}

	// 全てのゴルーチンの完了を待つ
	if err := g.Wait(); err != nil {
		// errgroup 内でエラーが発生した場合
		logger.Error("Error occurred during lock process", "error", err)
		return fmt.Errorf("lock command failed: %w", err)
	}

	// 新しいロックデータに既存のロックファイルの情報をマージする (新規エントリのみ)
	// SetHash 内でチェックしているので、明示的なマージは不要か？
	// -> SetHash がエラーを返すので、この時点で newLock は一貫性のある状態のはず。

	// 既存のロックファイルから、設定ファイルに存在しないエントリを削除 (Prune)
	// SetHash でチェックしているので、newLock に古いエントリは含まれないはずだが、
	// 念のため Prune を実行する。
	newLock.Prune(activeFiles)

	// 古いロックファイルと新しいロックファイルを比較し、変更があったか確認
	if reflect.DeepEqual(existingLock.Files, newLock.Files) {
		logger.Info("Lock file is already up to date.")
		return nil
	}

	// 新しいLockファイルを保存
	err = newLock.Save(configDir)
	if err != nil {
		return fmt.Errorf("failed to save lock file: %w", err)
	}

	logger.Info("Lock command finished successfully")
	return nil
}
