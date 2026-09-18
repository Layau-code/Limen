// commentcheck 校验生产 Go 方法是否带有用途注释。
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// main 检查命令行指定目录中的非测试 Go 文件。
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法: commentcheck <目录>...")
		os.Exit(2)
	}
	fset := token.NewFileSet()
	missing := 0
	for _, root := range os.Args[1:] {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				return err
			}
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Doc != nil {
					continue
				}
				position := fset.Position(function.Pos())
				fmt.Fprintf(os.Stderr, "%s:%d: 方法 %s 缺少用途注释\n", position.Filename, position.Line, function.Name.Name)
				missing++
			}
			return nil
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	if missing > 0 {
		os.Exit(1)
	}
}
