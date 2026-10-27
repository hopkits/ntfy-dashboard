// Package buildinfo 保存构建期注入的元信息。
//
// 版本号由构建脚本通过
//
//	-ldflags "-X github.com/ntfy-dashboard/ntfy-dashboard/internal/buildinfo.Version=v0.7"
//
// 注入；单独成包是为了让模板层能读到，而不必把 main 的变量透传一路。
package buildinfo

// Version 是程序版本号，未经注入时为 "dev"。
var Version = "dev"

// AppName 是界面上展示的产品名。
const AppName = "事件通知"

// Product 是产品次级标识，与版本号并排显示。
const Product = "Dashboard"
