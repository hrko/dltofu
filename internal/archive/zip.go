package archive

import (
	"archive/zip"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// ZipExtractor は Zip ファイルを展開する
type ZipExtractor struct{}

// Extract は Zip ファイルを展開するメソッド
func (z *ZipExtractor) Extract(sourcePath, destDir string, stripComponents int, extractPaths []string, force bool, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info("Extracting zip archive", "source", sourcePath, "destination", destDir, "strip", stripComponents, "force", force)

	r, err := zip.OpenReader(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to open zip file %s: %w", sourcePath, err)
	}
	defer r.Close()

	// 展開先ディレクトリが存在しない場合は作成
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory %s: %w", destDir, err)
	}

	for _, f := range r.File {
		// strip/extractPaths を考慮して展開すべきか、最終的な相対パスは何かを取得
		targetRelPath, should := shouldExtract(f.Name, stripComponents, extractPaths)
		if !should {
			logger.Debug("Skipping entry based on strip/extract paths", "original_path", f.Name)
			continue
		}

		// Zip Slip 攻撃を防ぎつつ、最終的な展開先パスを計算
		finalDestPath, err := secureJoin(destDir, targetRelPath)
		if err != nil {
			logger.Error("Skipping potentially unsafe path", "original_path", f.Name, "error", err)
			continue // 安全でないパスはスキップ
		}
		// logger.Debug("Processing archive entry", "original_path", f.Name, "target_relative_path", targetRelPath, "final_destination", finalDestPath)

		if f.FileInfo().IsDir() {
			// ディレクトリの場合
			proceed, err := checkOverwrite(finalDestPath, true, force, logger)
			if err != nil {
				return err // Statエラーなど
			}
			if !proceed {
				continue // 上書きしない場合はスキップ
			}
			logger.Debug("Creating directory", "path", finalDestPath)
			if err := os.MkdirAll(finalDestPath, f.Mode()); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", finalDestPath, err)
			}
		} else {
			// ファイルの場合
			proceed, err := checkOverwrite(finalDestPath, false, force, logger)
			if err != nil {
				return err
			}
			if !proceed {
				continue
			}

			// ディレクトリが存在しない場合は作成 (writeFile 内でも行うが念のため)
			if err := os.MkdirAll(filepath.Dir(finalDestPath), 0755); err != nil {
				return fmt.Errorf("failed to create directory for file %s: %w", finalDestPath, err)
			}

			rc, err := f.Open()
			if err != nil {
				return fmt.Errorf("failed to open file in zip archive %s: %w", f.Name, err)
			}

			logger.Debug("Extracting file", "path", finalDestPath, "mode", f.Mode())
			// writeFile 内で force フラグが考慮される
			err = writeFile(finalDestPath, rc, f.Mode(), force)
			rc.Close() // 必ず閉じる
			if err != nil {
				// writeFile 内で force=false によるエラーも含まれる
				if strings.Contains(err.Error(), "destination file already exists") {
					logger.Warn("Skipping existing file", "path", finalDestPath)
					continue // ログは checkOverwrite で出すのでここでは不要かも
				}
				return fmt.Errorf("failed to extract file %s: %w", f.Name, err)
			}
		}
	}
	logger.Info("Zip archive extracted successfully", "source", sourcePath)
	return nil
}
