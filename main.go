package main

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"cctui/internal/ccswitch"
	"cctui/internal/ui"
)

func main() {
	command, filePath, redactSecrets, err := parseCommandLine(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		printUsage(os.Stderr)
		os.Exit(2)
	}

	store, err := ccswitch.OpenStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开数据存储失败: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	if command == "import" {
		runImport(store, filePath)
		return
	}
	if command == "export" {
		runExport(store, filePath, redactSecrets)
		return
	}

	warnings, err := store.Bootstrap()
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化失败: %v\n", err)
		os.Exit(1)
	}

	model, err := ui.NewModel(store, warnings)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化界面失败: %v\n", err)
		os.Exit(1)
	}

	program := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "程序运行失败: %v\n", err)
		os.Exit(1)
	}
}

func parseCommandLine(args []string) (command, filePath string, redactSecrets bool, err error) {
	if len(args) == 0 {
		return "", "", false, nil
	}
	if args[0] == "import" || args[0] == "--import-file" {
		if len(args) != 2 || strings.TrimSpace(args[1]) == "" {
			return "", "", false, fmt.Errorf("import 需要一个 JSON/YAML 文件路径")
		}
		return "import", args[1], false, nil
	}
	if args[0] == "export" || args[0] == "--export-file" {
		if len(args) < 2 || len(args) > 3 || strings.TrimSpace(args[1]) == "" {
			return "", "", false, fmt.Errorf("export 需要一个 JSON/YAML 文件路径")
		}
		redactSecrets := false
		if len(args) == 3 {
			if args[2] != "--redact-secrets" {
				return "", "", false, fmt.Errorf("未知 export 参数: %s", args[2])
			}
			redactSecrets = true
		}
		return "export", args[1], redactSecrets, nil
	}
	return "", "", false, fmt.Errorf("未知参数: %s", args[0])
}

func runImport(store *ccswitch.Store, path string) {
	result, err := store.ImportProviderFile(path)
	for _, item := range result.Imported {
		fmt.Printf("已追加 %s\n", item)
	}
	for _, item := range result.Updated {
		fmt.Printf("已覆盖 %s\n", item)
	}
	for _, item := range result.Failed {
		fmt.Fprintf(os.Stderr, "导入失败 %s\n", item)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func runExport(store *ccswitch.Store, path string, redactSecrets bool) {
	count, err := store.ExportProviderFile(path, ccswitch.ExportOptions{RedactSecrets: redactSecrets})
	if err != nil {
		fmt.Fprintf(os.Stderr, "导出失败: %v\n", err)
		os.Exit(1)
	}
	if redactSecrets {
		fmt.Printf("已导出 %d 个供应商（敏感字段已脱敏）: %s\n", count, path)
		return
	}
	fmt.Printf("已导出 %d 个供应商: %s\n", count, path)
}

func printUsage(output *os.File) {
	fmt.Fprintln(output, "用法:")
	fmt.Fprintln(output, "  cctui")
	fmt.Fprintln(output, "  cctui import <providers.json|providers.yml|providers.yaml>")
	fmt.Fprintln(output, "  cctui --import-file <providers.json|providers.yml|providers.yaml>")
	fmt.Fprintln(output, "  cctui export <providers.json|providers.yml|providers.yaml> [--redact-secrets]")
	fmt.Fprintln(output, "  cctui --export-file <providers.json|providers.yml|providers.yaml> [--redact-secrets]")
}
