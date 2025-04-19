package archive

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Extractor はアーカイブを展開するインターフェース
type Extractor interface {
	Extract(sourcePath, destDir string, stripComponents int, extractPaths []string, force bool, logger *slog.Logger) error
}

// CommonExtractOptions は展開時の共通オプション (現在は未使用だが将来的に)
// type CommonExtractOptions struct {
// 	Force bool
// }

// GetExtractor はファイルパスの拡張子に基づいて適切な Extractor を返す
func GetExtractor(filePath string) (Extractor, error) {
	lowerPath := strings.ToLower(filePath)
	if strings.HasSuffix(lowerPath, ".zip") {
		return &ZipExtractor{}, nil
	}
	if strings.HasSuffix(lowerPath, ".tar.gz") || strings.HasSuffix(lowerPath, ".tgz") {
		return &TarGzExtractor{}, nil
	}
	// 他の形式 (e.g., .tar.bz2, .tar.xz) を追加する場合はここに追記
	return nil, fmt.Errorf("unsupported archive format for file: %s", filePath)
}

// --- Helper functions ---

// secureJoin は filepath.Join と似ているが、Zip Slip 攻撃を防ぐ
// destDir 外へのパス "../" などが含まれていないかチェックする
func secureJoin(destDir, targetPath string) (string, error) {
	joinedPath := filepath.Join(destDir, targetPath)
	if !strings.HasPrefix(joinedPath, filepath.Clean(destDir)+string(os.PathSeparator)) && joinedPath != filepath.Clean(destDir) {
		// joinedPath が destDir の外を指している場合
		return "", fmt.Errorf("invalid path in archive: '%s' attempts to escape destination directory", targetPath)
	}
	return joinedPath, nil
}

// stripPathComponents はパス文字列から指定された数の先頭コンポーネントを削除する
func stripPathComponents(path string, count int) string {
	if count <= 0 {
		return path
	}
	// Clean で余分な "/" を除去し、"/" で分割
	components := strings.Split(filepath.Clean(path), string(os.PathSeparator))
	if len(components) <= count {
		return "" // 全て削除されるか、それ以上削除する場合
	}
	// count 番目以降のコンポーネントを結合
	return filepath.Join(components[count:]...)
}

// shouldExtract は strip/extractPaths を考慮してファイル/ディレクトリを展開すべきか判断する
func shouldExtract(originalPath string, stripComponents int, extractPaths []string) (string, bool) {
	strippedPath := stripPathComponents(originalPath, stripComponents)
	if strippedPath == "" {
		return "", false // パスが空になった場合はスキップ
	}

	if len(extractPaths) == 0 {
		return strippedPath, true // extractPaths がなければ常に展開
	}

	// extractPaths が指定されている場合、前方一致でチェック
	for _, pattern := range extractPaths {
		pattern = filepath.Clean(pattern) // パターンも正規化
		// 1. 完全一致
		if strippedPath == pattern {
			return strippedPath, true
		}
		// 2. ディレクトリ指定の場合 (パターンが "/" で終わるか、strippedPath がパターン + "/" で始まる)
		if strings.HasSuffix(pattern, string(os.PathSeparator)) {
			if strings.HasPrefix(strippedPath, pattern) {
				return strippedPath, true
			}
		} else {
			// ファイル指定の場合、ディレクトリ内の一致も考慮
			if strings.HasPrefix(strippedPath, pattern+string(os.PathSeparator)) {
				return strippedPath, true
			}
		}
	}

	return "", false // どのパターンにも一致しない
}

// writeFile は io.Reader の内容をディスク上のファイルに書き込む
// force が false の場合、ファイルが既に存在するとエラーを返す
func writeFile(destPath string, reader io.Reader, mode os.FileMode, force bool) error {
	if !force {
		if _, err := os.Stat(destPath); err == nil {
			// ファイルが存在し、force=false ならエラー
			return fmt.Errorf("destination file already exists: %s (use --force to overwrite)", destPath)
		} else if !os.IsNotExist(err) {
			// Stat で予期せぬエラー
			return fmt.Errorf("failed to check destination file %s: %w", destPath, err)
		}
		// ファイルが存在しない場合は続行
	} else {
		// force=true の場合、既存ファイルを削除してから作成 (os.Createがトランケートするため不要かも)
		// _, err := os.Stat(destPath)
		// if err == nil {
		//     if err := os.Remove(destPath); err != nil {
		//          return fmt.Errorf("failed to remove existing file %s for overwrite: %w", destPath, err)
		//     }
		// } else if !os.IsNotExist(err) {
		//     return fmt.Errorf("failed to check destination file %s before overwrite: %w", destPath, err)
		// }
	}

	// ディレクトリが存在しない場合は作成
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", filepath.Dir(destPath), err)
	}

	outFile, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("failed to open destination file %s for writing: %w", destPath, err)
	}
	defer outFile.Close()

	_, err = io.Copy(outFile, reader)
	if err != nil {
		// 書き込み中にエラーが発生した場合、中途半端なファイルを削除する方が親切かも
		_ = os.Remove(destPath)
		return fmt.Errorf("failed to write to destination file %s: %w", destPath, err)
	}

	return nil
}

// checkOverwrite はファイル/ディレクトリの上書きを確認する (インタラクティブ or --force)
// このサンプル実装ではインタラクティブな確認は省略し、force フラグのみ考慮
func checkOverwrite(destPath string, isDir, force bool, logger *slog.Logger) (bool, error) {
	stat, err := os.Stat(destPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil // 存在しないので上書きOK (新規作成)
		}
		return false, fmt.Errorf("failed to check destination %s: %w", destPath, err)
	}

	// 存在する場合
	if force {
		logger.Debug("Overwriting existing path due to --force", "path", destPath)
		// ディレクトリを上書きする場合、中身を削除する必要があるかもしれない
		// ここでは単純化のため、個々のファイル書き込み時に force が考慮されることに期待
		// ただし、ファイル -> ディレクトリ or ディレクトリ -> ファイルの上書きは厄介
		if stat.IsDir() != isDir {
			return false, fmt.Errorf("cannot overwrite path %s: type mismatch (file/directory)", destPath)
		}
		return true, nil // force=true なら上書きOK
	} else {
		// force=false で存在する場合
		logger.Warn("Skipping extraction: destination path already exists. Use --force to overwrite.", "path", destPath)
		return false, nil // 上書きしない
	}
}
