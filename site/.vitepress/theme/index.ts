/**
 * 全站主题入口：继承 VitePress 默认主题，只做两件事。
 *
 * 1) doc-before 插槽补一个 h1 —— 内容源只写 frontmatter.title，正文不写 `# 标题`；
 * 2) 全局注册 HomeSections，首页 markdown 直接写 <HomeSections />。
 */

import { h } from 'vue'
import type { Theme } from 'vitepress'
import DefaultTheme from 'vitepress/theme'

import HomeSections from './components/HomeSections.vue'
import DocTitle from './components/DocTitle.vue'

import './style.css'

export default {
	extends: DefaultTheme,
	Layout() {
		return h(DefaultTheme.Layout, null, {
			// 内容源不写 # 标题，课时页的 h1 由 frontmatter.title 补上
			'doc-before': () => h(DocTitle),
		})
	},
	enhanceApp({ app }) {
		app.component('HomeSections', HomeSections)
	},
} satisfies Theme
