import { readdirSync, readFileSync, existsSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vitepress'
import { withMermaid } from 'vitepress-plugin-mermaid'

import { computeStats } from './stats.mts'

// GitHub Pages：https://lerixhe.github.io/idc-network-observability/
const SITE = 'https://lerixhe.github.io'
const BASE = '/idc-network-observability/'
const REPO = 'https://github.com/lerixhe/idc-network-observability'
const DOCS_DIR = fileURLToPath(new URL('../docs', import.meta.url))

// 课时数 / 模块数 / 实验数从文件系统数出来，不手写
const stats = computeStats(DOCS_DIR, fileURLToPath(new URL('../../labs', import.meta.url)))

/**
 * 侧边栏模块表：模块级手写 label，模块内自动生成。
 * 新增课时（目录内加文件）无需改本文件；新增模块才需要加一行。
 * 只挂载已有内容的模块，避免侧边栏出现空分组。
 */
const MODULES = [
	{ dir: '00-intro', text: '第一部分 · 认识现场' },
	{ dir: '01-ports', text: '第二部分 · 端口与设备存活' },
	{ dir: '02-probe', text: '第三部分 · 主动链路探测' },
	{ dir: '03-snmp', text: '第四部分 · 设备指标采集' },
	{ dir: '04-flow', text: '第五部分 · 流数据' },
	{ dir: '05-cmdb', text: '第六部分 · CMDB 与拓扑' },
	{ dir: '06-platform', text: '第七部分 · 平台工程' },
	{ dir: '07-alert', text: '第八部分 · 指标与告警' },
	{ dir: '08-troubleshoot', text: '第九部分 · 排障与定位' },
	{ dir: '09-case', text: '第十部分 · 实战' },
	{ dir: 'appendix', text: '附录' },
]

/** 从 frontmatter 里取 title，取不到就退回文件名 */
function readTitle(file: string, fallback: string): string {
	const head = readFileSync(file, 'utf8').slice(0, 2000)
	const m = head.match(/^title:\s*(.+)$/m)
	return m ? m[1].trim().replace(/^["']|["']$/g, '') : fallback
}

function moduleItems(dir: string) {
	const abs = join(DOCS_DIR, dir)
	if (!existsSync(abs)) return []
	return readdirSync(abs)
		.filter((f) => f.endsWith('.md') && f !== 'index.md')
		.sort()
		.map((f) => {
			const base = f.replace(/\.md$/, '')
			return {
				text: readTitle(join(abs, f), base),
				link: `/${dir}/${base}`,
			}
		})
}

const sidebar = [
	{ text: '本书首页', link: '/' },
	...MODULES.map(({ dir, text }) => {
		const items = moduleItems(dir)
		// 模块正文的 index.md 也挂上，骨架期先靠它交代本篇要讲什么
		const entry = existsSync(join(DOCS_DIR, dir, 'index.md'))
			? [{ text: '篇首 · 总览', link: `/${dir}/` }]
			: []
		// collapsed: true —— 五十六项全展开会把首屏铺满一屏半，读者看不到自己站在哪。
		// VitePress 会自动展开含当前页的那一组（VPSidebarItem 的 watchPostEffect），所以当前篇不用手写展开。
		return items.length + entry.length ? { text, collapsed: true, items: [...entry, ...items] } : null
	}).filter((x): x is NonNullable<typeof x> => x !== null),
]

const config = defineConfig({
	title: 'IDC 网络可观测指南',
	description:
		'IDC 网络可观测指南：端口状态、主动探测、SNMP、流数据、CMDB 与拓扑、平台工程、指标告警、排障定位。写给后端开发的可观测工程第一课，每个机制配可复现的最小实验。',
	lang: 'zh-CN',
	base: BASE,
	srcDir: './docs',
	cleanUrls: true,
	lastUpdated: true,
	sitemap: { hostname: SITE + BASE },

	head: [
		// public/ 下的文件会落到 base 根，所以这里要自己拼上前缀
		['link', { rel: 'icon', type: 'image/svg+xml', href: `${BASE}favicon.svg` }],
		// 移动端地址栏配色，跟随系统明暗
		['meta', { name: 'theme-color', content: '#0b0e14', media: '(prefers-color-scheme: dark)' }],
		['meta', { name: 'theme-color', content: '#ffffff', media: '(prefers-color-scheme: light)' }],
		// 中文站：显式声明语言，利于搜索引擎与阅读器
		['meta', { property: 'og:locale', content: 'zh_CN' }],
	],

	themeConfig: {
		// 首页指标条读这个，不要在组件里再写死数字（见 stats.mts）
		idcStats: stats,

		// 导航栏不显示站名，回首页入口下放到侧边栏首项
		siteTitle: false,
		outline: { level: [2, 3], label: '本页目录' },
		socialLinks: [{ icon: 'github', link: REPO }],
		editLink: {
			pattern: `${REPO}/edit/main/site/docs/:path`,
			text: '在 GitHub 上编辑此页',
		},
		lastUpdated: {
			text: '最后更新于',
			formatOptions: { dateStyle: 'medium' },
		},
		docFooter: { prev: '上一课', next: '下一课' },
		sidebarMenuLabel: '目录',
		returnToTopLabel: '回到顶部',
		darkModeSwitchLabel: '主题',
		sidebar,

		search: {
			provider: 'local',
			options: {
				translations: {
					button: {
						buttonText: '搜索文档',
						buttonAriaLabel: '搜索文档',
					},
					modal: {
						displayDetails: '显示详情',
						resetButtonTitle: '清除查询',
						resetButtonAriaLabel: '清除查询',
						noResultsText: '没有找到结果',
						footer: {
							selectText: '选择',
							navigateText: '切换',
							closeText: '关闭',
						},
					},
				},
			},
		},
	},
})

export default withMermaid(config)
