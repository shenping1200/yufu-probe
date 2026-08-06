package main

import "time"

// 全站统一的「业务时区」：固定东八区（北京时间）。
//
// 为什么不用容器 TZ 环境变量 / tzdata：
//   - 服务端基础镜像是 alpine，默认不带 tzdata，time.Local 会退化成 UTC；
//   - 若依赖 docker-compose 的 TZ 变量，一旦有人换宿主机、换编排文件或直接
//     docker run 起容器，月份切换点就会悄悄漂回 UTC（相当于北京时间早上 8 点
//     才重置流量），属于"看不见的错"。
//
// 因此把时区写死在代码里，零外部依赖，任何环境下行为一致。
var bizLoc = time.FixedZone("CST", 8*3600)

// bizNow 返回北京时间的当前时刻。
func bizNow() time.Time {
	return time.Now().In(bizLoc)
}

// monthOf 返回给定时刻在北京时间下的月份键（格式 2006-01）。
func monthOf(t time.Time) string {
	return t.In(bizLoc).Format("2006-01")
}

// curMonth 返回北京时间当前的月份键（格式 2006-01）。
//
// 这是流量按自然月分区的唯一口径：traffic_monthly 表的 year_month 列、
// LoadFromDB / Flush / ListAgents 的月份参数都必须走这里，
// 保证"每月 1 日 0 点（北京时间）重置本月流量"的切换点全局一致。
func curMonth() string {
	return monthOf(time.Now())
}
