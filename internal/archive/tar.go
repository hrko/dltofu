package archive

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// TarGzExtractor は Tar.gz ファイルを展開する
type TarGzExtractor struct{}

// Extract は Tar.gz ファイルを展開するメソッド
func (t *TarGzExtractor) Extract(sourcePath, destDir string, stripComponents int, extractPaths []string, force bool, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info("Extracting tar.gz archive", "source", sourcePath, "destination", destDir, "strip", stripComponents, "force", force)

	file, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to open tar.gz file %s: %w", sourcePath, err)
	}
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader for %s: %w", sourcePath, err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	// 展開先ディレクトリが存在しない場合は作成
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory %s: %w", destDir, err)
	}

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break // ファイルの終端
		}
		if err != nil {
			return fmt.Errorf("failed to read tar header: %w", err)
		}

		// strip/extractPaths を考慮して展開すべきか、最終的な相対パスは何かを取得
		targetRelPath, should := shouldExtract(header.Name, stripComponents, extractPaths)
		if !should {
			logger.Debug("Skipping entry based on strip/extract paths", "original_path", header.Name)
			continue
		}

		// Zip Slip 攻撃を防ぎつつ、最終的な展開先パスを計算
		finalDestPath, err := secureJoin(destDir, targetRelPath)
		if err != nil {
			logger.Error("Skipping potentially unsafe path", "original_path", header.Name, "error", err)
			continue
		}
		// logger.Debug("Processing archive entry", "original_path", header.Name, "target_relative_path", targetRelPath, "final_destination", finalDestPath)

		// Tar ヘッダ情報からファイルモードを取得
		mode := header.FileInfo().Mode()

		switch header.Typeflag {
		case tar.TypeDir:
			// ディレクトリの場合
			proceed, err := checkOverwrite(finalDestPath, true, force, logger)
			if err != nil {
				return err
			}
			if !proceed {
				continue
			}
			logger.Debug("Creating directory", "path", finalDestPath, "mode", mode)
			if err := os.MkdirAll(finalDestPath, mode); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", finalDestPath, err)
			}
		case tar.TypeReg:
			// 通常ファイルの場合
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

			logger.Debug("Extracting file", "path", finalDestPath, "mode", mode)
			// writeFile 内で force フラグが考慮される
			err = writeFile(finalDestPath, tr, mode, force) // tr (tar.Reader) は io.Reader を満たす
			if err != nil {
				if strings.Contains(err.Error(), "destination file already exists") {
					logger.Warn("Skipping existing file", "path", finalDestPath)
					continue
				}
				return fmt.Errorf("failed to extract file %s: %w", header.Name, err)
			}
		case tar.TypeSymlink:
			// シンボリックリンクの場合 (注意: セキュリティリスクの可能性)
			proceed, err := checkOverwrite(finalDestPath, false, force, logger) // Link もファイルとして扱う
			if err != nil {
				return err
			}
			if !proceed {
				continue
			}
			logger.Info("Creating symlink", "link_path", finalDestPath, "target", header.Linkname)
			// 既存のリンクがあれば削除 (os.Symlink は上書きしないため)
			if _, lstatErr := os.Lstat(finalDestPath); lstatErr == nil {
				if err := os.Remove(finalDestPath); err != nil {
					return fmt.Errorf("failed to remove existing symlink %s: %w", finalDestPath, err)
				}
			} else if !os.IsNotExist(lstatErr) {
				return fmt.Errorf("failed to check existing symlink %s: %w", finalDestPath, lstatErr)
			}
			if err := os.Symlink(header.Linkname, finalDestPath); err != nil {
				return fmt.Errorf("failed to create symlink %s -> %s: %w", finalDestPath, header.Linkname, err)
			}
			// TODO: シンボリックリンクのパーミッション設定は os.Symlink ではできない

		// 他のタイプ (TypeLink, TypeChar, TypeBlock, TypeFifo) は必要に応じて対応
		default:
			logger.Warn("Unsupported tar entry type", "type", header.Typeflag, "name", header.Name)
		}
	}
	logger.Info("Tar.gz archive extracted successfully", "source", sourcePath)
	return nil
}
