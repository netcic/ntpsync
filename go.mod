module ntpsync

// 语言版本固定 1.20，不是笔误：Windows 产物必须用 Go 1.20.14 编译才能跑在 Win7 上。
// 从 Go 1.21 起，Windows 运行时链接 bcryptprimitives.dll!ProcessPrng，该导出在 Win7 不存在，
// 二进制会在加载阶段直接失败（表现为安装器报「服务注册失败」，因为程序根本没起来）。
// 声明 1.20 有两个作用：一是允许 1.20 工具链编译本模块，二是让各平台使用一致的
// 语言语义（例如 <1.22 的循环变量共享），避免 Windows 与 Linux 行为分叉。
// 若哪天不再需要 Win7，把这里改回最新版本，并同步去掉 scripts/build.sh 里的 WIN_GO。
go 1.20

require golang.org/x/sys v0.24.0 // indirect
