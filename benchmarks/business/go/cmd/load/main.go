// load 对本机业务应用执行可复现的开放或闭环负载，并保存完整测量结果。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/loadrun"
)

const defaultSecretEnvironment = "BENCH_SECRET"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	status := run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv)
	stop()
	os.Exit(status)
}

func run(ctx context.Context, arguments []string, stdout, stderr io.Writer, lookup func(string) string) int {
	configuration := loadrun.DefaultConfig()
	flags := flag.NewFlagSet("load", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var secretEnvironment, output string
	flags.StringVar(&configuration.URL, "url", configuration.URL, "被测应用的 loopback HTTP 根地址")
	flags.StringVar(&configuration.Scenario, "scenario", configuration.Scenario, "catalog/detail/order/ticket/mixed，混合权重40/30/20/10")
	flags.Float64Var(&configuration.Rate, "rate", configuration.Rate, "每秒计划请求数；正数为开放负载，0为闭环")
	flags.IntVar(&configuration.Concurrency, "concurrency", configuration.Concurrency, "闭环固定并发请求数")
	flags.IntVar(&configuration.MaxInflight, "max-inflight", configuration.MaxInflight, "开放负载最大在途数，超出记录为dropped")
	flags.DurationVar(&configuration.Duration, "duration", configuration.Duration, "正式发送窗口，不包含在途请求排空")
	flags.DurationVar(&configuration.Timeout, "timeout", configuration.Timeout, "单次请求从发送到读取正文的超时")
	flags.Uint64Var(&configuration.Seed, "seed", configuration.Seed, "可复现业务请求种子")
	flags.StringVar(&configuration.RunID, "run-id", "", "本轮唯一标识，用于生成订单幂等键；必填")
	flags.StringVar(&secretEnvironment, "secret-env", defaultSecretEnvironment, "认证密钥所在环境变量名称，密钥不写入结果")
	flags.StringVar(&output, "output", "", "完整结果文件路径，支持.json/.json.gz且不覆盖已有文件；必填")
	flags.IntVar(&configuration.TenantCount, "tenant-count", configuration.TenantCount, "种子数据中的租户数量")
	flags.IntVar(&configuration.UsersPerTenant, "users-per-tenant", configuration.UsersPerTenant, "每租户用户数量")
	flags.IntVar(&configuration.ProductsPerTenant, "products-per-tenant", configuration.ProductsPerTenant, "每租户商品数量")
	flags.IntVar(&configuration.TicketsPerTenant, "tickets-per-tenant", configuration.TicketsPerTenant, "每租户工单数量")
	flags.IntVar(&configuration.Categories, "categories", configuration.Categories, "每租户分类数量")
	flags.IntVar(&configuration.ErrorSampleLimit, "error-samples", configuration.ErrorSampleLimit, "有界错误样本数量，正文与认证头不会保存")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || output == "" || secretEnvironment == "" {
		fmt.Fprintln(stderr, "必须指定-output，不接受位置参数，-secret-env不能为空")
		return 2
	}
	configuration.Secret = lookup(secretEnvironment)
	if err := configuration.Validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	// 在发起业务写入前检查结果路径，避免重复命令运行后才发现无法保存。
	if _, err := os.Lstat(output); err == nil || !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stderr, "输出文件已存在或无法检查，拒绝启动测量")
		return 1
	}
	directory, err := os.Stat(filepath.Dir(output))
	if err != nil || !directory.IsDir() {
		fmt.Fprintln(stderr, "输出目录不存在或不是普通目录")
		return 1
	}
	report, runErr := loadrun.Run(ctx, configuration)
	if err := loadrun.SaveReport(output, report); err != nil {
		fmt.Fprintf(stderr, "保存结果失败: %v\n", err)
		return 1
	}
	summary := struct {
		RunID          string           `json:"run_id"`
		Model          string           `json:"model"`
		Scenario       string           `json:"scenario"`
		WindowSeconds  float64          `json:"window_seconds"`
		ElapsedSeconds float64          `json:"elapsed_seconds"`
		Totals         loadrun.Counters `json:"totals"`
		SuccessRPS     float64          `json:"success_rps"`
		IssuedRPS      float64          `json:"issued_success_rps"`
		P99MS          float64          `json:"p99_ms"`
		SchedulerP99MS float64          `json:"scheduler_p99_ms"`
		Canceled       bool             `json:"canceled"`
		Output         string           `json:"output"`
	}{
		RunID: configuration.RunID, Model: report.Model, Scenario: configuration.Scenario,
		WindowSeconds: report.WindowSeconds, ElapsedSeconds: report.ElapsedSeconds,
		Totals: report.Totals, SuccessRPS: report.SuccessRPS, IssuedRPS: report.IssuedSuccessRPS,
		P99MS: report.Latency.P99MS, SchedulerP99MS: report.SchedulerLag.P99MS, Canceled: report.Canceled, Output: output,
	}
	if err := json.NewEncoder(stdout).Encode(summary); err != nil {
		fmt.Fprintf(stderr, "输出摘要失败: %v\n", err)
		return 1
	}
	if runErr != nil {
		fmt.Fprintf(stderr, "测量中止，已保存部分结果: %v\n", runErr)
		return 1
	}
	return 0
}
