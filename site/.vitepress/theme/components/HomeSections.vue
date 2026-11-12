<script setup lang="ts">
/**
 * 首页主体内容：四篇的路径卡片 + 三段说明 + 系列导航。
 * 样式全部以 .lp- 前缀限定作用域，定义在 style.css。
 *
 * 卡片里不写"X 课"这类数字——课时数随时会变，手写必漂移；
 * 要在首页展示规模，读 theme.idcStats（构建期从文件系统数出来）。
 */

import { withBase } from 'vitepress'

const modules = [
	{
		id: '开篇',
		title: '先把地图铺开',
		desc: '一个 IDC 里到底有哪些东西、后端熟悉的那套监控为什么在这块地界不够用、全书四个环节怎么接起来。',
		href: '/00-intro/00-why',
	},
	{
		id: '第一篇',
		title: '采集',
		desc: '数据从哪来：设备指标、主动探测、流数据、带内遥测。写一个 Go 探针，从零拿到第一条链路时延。',
		href: '/01-collect/',
	},
	{
		id: '第二篇',
		title: '指标',
		desc: '测什么、怎么算、怎么存：时延丢包抖动的口径，标签与维度设计，Kafka 到 ClickHouse 的管道，基线从哪里来。',
		href: '/02-metrics/',
	},
	{
		id: '第三篇',
		title: '判读',
		desc: '一条曲线抖动到什么程度才算故障：阈值与抑制、常见异常形态、ECMP 哈希不均、微突发与采样失真。',
		href: '/03-read/',
	},
	{
		id: '第四篇',
		title: '定位',
		desc: '从"某个业务变慢"走到"某块光模块劣化"：分段二分、路径追踪、变更窗口，最后一个端到端案例收口。',
		href: '/04-locate/',
	},
]

const facts = [
	{
		kicker: '写给谁',
		title: '写业务代码的人',
		body: '会 Go 或 Java、看过监控面板、知道 TCP 三次握手，但没配过交换机。你不需要网络工程师的证件，只需要愿意读 pcap。',
	},
	{
		kicker: '去往哪',
		title: '可观测平台的研发岗',
		body: '写采集器、写数据管道、写告警规则、写查询接口的人。网络只是题材，交付物是能跑的系统。',
	},
	{
		kicker: '怎么读',
		title: '四篇一条线，实验在 labs',
		body: '每篇收一个可复现的最小实验，默认零外部依赖，一台 Mac 就能跑。读完四篇，能独立搭一套能报警的链路观测。',
	},
]
</script>

<template>
	<div class="lp-content">
		<section class="lp-section" aria-labelledby="lp-path">
			<header class="lp-head">
				<p class="lp-kicker">阅读路径</p>
				<h2 id="lp-path">四篇，一条从数据到结论的线</h2>
				<p class="lp-lede">每一篇交出一个能跑的东西，下一篇接着用。不跳读也能走完，跳读会少几块拼图。</p>
			</header>

			<ol class="lp-cards">
				<li v-for="m in modules" :key="m.id">
					<a class="lp-card" :href="withBase(m.href)">
						<span class="lp-card__top">
							<span class="lp-card__id">{{ m.id }}</span>
						</span>
						<span class="lp-card__title">{{ m.title }}</span>
						<span class="lp-card__desc">{{ m.desc }}</span>
						<span class="lp-card__link">
							进入
							<svg viewBox="0 0 16 16" width="13" height="13" aria-hidden="true">
								<path
									d="M3 8h9M8.5 4.5 12 8l-3.5 3.5"
									fill="none"
									stroke="currentColor"
									stroke-width="1.6"
									stroke-linecap="round"
									stroke-linejoin="round"
								/>
							</svg>
						</span>
					</a>
				</li>
			</ol>
		</section>

		<section class="lp-section lp-facts" aria-label="本书说明">
			<article v-for="f in facts" :key="f.title">
				<p class="lp-kicker">{{ f.kicker }}</p>
				<h3>{{ f.title }}</h3>
				<p class="lp-body">{{ f.body }}</p>
			</article>
		</section>

		<section class="lp-section" aria-labelledby="lp-series">
			<header class="lp-head">
				<p class="lp-kicker">系列</p>
				<h2 id="lp-series">网络可观测，两册</h2>
				<p class="lp-lede">本册讲有损网络：丢包是背景噪声，观测的重点是稳定与体验。第二册讲无损智算：丢包就是事故，整套假设要换一遍。</p>
			</header>

			<ul class="lp-labs">
				<li>
					<a class="lp-lab" :href="withBase('/00-intro/00-why')">
						<span class="lp-lab__name">第一册 · IDC 网络可观测入门</span>
						<span class="lp-lab__desc">你正在读的这一本。传统 IDC，有损网络，从采集到定位。</span>
					</a>
				</li>
				<li>
					<a class="lp-lab" href="https://lerixhe.github.io/aidc-observability/">
						<span class="lp-lab__name">第二册 · 智算网络可观测指南</span>
						<span class="lp-lab__desc">从有损 IDC 到无损智算：机制、遥测与可运行的实验室。</span>
					</a>
				</li>
			</ul>
		</section>
	</div>
</template>

<style scoped>
.lp-content {
	display: flex;
	flex-direction: column;
	gap: clamp(3rem, 7vw, 4.5rem);
}

.lp-section {
	display: flex;
	flex-direction: column;
	gap: 1.75rem;
}

.lp-head {
	display: flex;
	flex-direction: column;
	gap: 0.6rem;
	max-width: 46rem;
}

.lp-kicker {
	margin: 0;
	font-size: var(--sl-text-xs);
	font-weight: 600;
	letter-spacing: 0.14em;
	text-transform: uppercase;
	color: var(--lp-accent);
}

h2 {
	margin: 0;
	padding: 0;
	border-top: none;
	font-size: clamp(1.35rem, 1.4vw + 0.9rem, 1.75rem);
	font-weight: 650;
	letter-spacing: -0.015em;
	line-height: 1.25;
	color: var(--lp-fg);
}

.lp-lede {
	margin: 0;
	color: var(--lp-muted);
	font-size: var(--sl-text-base);
	line-height: 1.7;
}

/* ---------- 模块卡片 ---------- */

.lp-cards {
	display: grid;
	grid-template-columns: repeat(auto-fit, minmax(min(100%, 18rem), 1fr));
	gap: 1rem;
	margin: 0;
	padding: 0;
	list-style: none;
}

.lp-card {
	position: relative;
	display: flex;
	flex-direction: column;
	gap: 0.7rem;
	height: 100%;
	padding: 1.35rem 1.35rem 1.15rem;
	border: 1px solid var(--lp-border);
	border-radius: var(--lp-radius);
	background: var(--lp-surface);
	text-decoration: none;
	transition:
		transform 0.2s ease,
		border-color 0.2s ease,
		box-shadow 0.2s ease,
		background-color 0.2s ease;
}

/* hover 时顶部的一道极细高光 */
.lp-card::before {
	content: '';
	position: absolute;
	inset-inline: 1.35rem;
	top: -1px;
	height: 1px;
	background: linear-gradient(90deg, transparent, var(--lp-accent), transparent);
	opacity: 0;
	transition: opacity 0.2s ease;
}

.lp-card:hover {
	transform: translateY(-3px);
	border-color: var(--lp-border-strong);
	box-shadow: var(--lp-shadow);
}

.lp-card:hover::before {
	opacity: 0.9;
}

.lp-card__top {
	display: flex;
	align-items: center;
	justify-content: space-between;
}

.lp-card__id {
	font-family: var(--sl-font-mono);
	font-size: var(--sl-text-xs);
	font-weight: 600;
	letter-spacing: 0.08em;
	color: var(--lp-accent);
}

.lp-card__count {
	font-size: var(--sl-text-2xs);
	color: var(--lp-muted);
	padding: 0.1rem 0.5rem;
	border: 1px solid var(--lp-border);
	border-radius: 999px;
}

.lp-card__title {
	font-size: var(--sl-text-lg);
	font-weight: 650;
	line-height: 1.35;
	letter-spacing: -0.01em;
	color: var(--lp-fg);
}

.lp-card__desc {
	font-size: var(--sl-text-sm);
	line-height: 1.7;
	color: var(--lp-muted);
}

.lp-card__link {
	display: inline-flex;
	align-items: center;
	gap: 0.35rem;
	margin-block-start: auto;
	padding-block-start: 0.35rem;
	font-size: var(--sl-text-xs);
	font-weight: 600;
	color: var(--lp-fg);
	opacity: 0.55;
	transition: opacity 0.2s ease;
}

.lp-card__link svg {
	transition: transform 0.2s ease;
}

.lp-card:hover .lp-card__link {
	opacity: 1;
}

.lp-card:hover .lp-card__link svg {
	transform: translateX(3px);
}

/* ---------- 三段说明 ---------- */

.lp-facts {
	display: grid;
	grid-template-columns: repeat(auto-fit, minmax(min(100%, 16rem), 1fr));
	gap: clamp(1.5rem, 4vw, 2.5rem);
	padding-block-start: clamp(1.5rem, 4vw, 2.25rem);
	border-top: 1px solid var(--lp-border);
}

.lp-facts article {
	display: flex;
	flex-direction: column;
	gap: 0.5rem;
}

.lp-facts h3 {
	margin: 0;
	padding: 0;
	border-top: none;
	font-size: var(--sl-text-base);
	font-weight: 650;
	letter-spacing: -0.01em;
	color: var(--lp-fg);
}

.lp-body {
	margin: 0;
	font-size: var(--sl-text-sm);
	line-height: 1.75;
	color: var(--lp-muted);
}

/* ---------- 收尾 CTA ---------- */

.lp-cta {
	position: relative;
	display: flex;
	flex-wrap: wrap;
	align-items: center;
	justify-content: space-between;
	gap: 1.5rem;
	padding: clamp(1.5rem, 4vw, 2.25rem);
	border: 1px solid var(--lp-border);
	border-radius: calc(var(--lp-radius) + 6px);
	background:
		radial-gradient(28rem 16rem at 8% 0%, var(--lp-glow-a), transparent 70%),
		var(--lp-surface);
	overflow: hidden;
}

.lp-cta__text {
	display: flex;
	flex-direction: column;
	gap: 0.5rem;
	max-width: 34rem;
}

.lp-cta__text p {
	margin: 0;
	font-size: var(--sl-text-sm);
	line-height: 1.7;
	color: var(--lp-muted);
}

.lp-cta__btn {
	display: inline-flex;
	align-items: center;
	gap: 0.45rem;
	padding: 0.7rem 1.35rem;
	border-radius: 999px;
	background: var(--lp-fg);
	color: var(--lp-surface-solid);
	font-size: var(--sl-text-sm);
	font-weight: 600;
	text-decoration: none;
	white-space: nowrap;
	transition:
		transform 0.18s ease,
		box-shadow 0.18s ease;
}

.lp-cta__btn:hover {
	transform: translateY(-1px);
	box-shadow: 0 14px 30px -16px hsl(220 60% 4% / 0.7);
}

.lp-cta__btn svg {
	transition: transform 0.18s ease;
}

.lp-cta__btn:hover svg {
	transform: translateX(3px);
}

.lp-note {
	margin: 0;
	padding-block-start: 1.25rem;
	border-top: 1px solid var(--lp-border);
	font-size: var(--sl-text-xs);
	line-height: 1.7;
	color: var(--lp-muted);
	opacity: 0.8;
}

@media (prefers-reduced-motion: reduce) {
	.lp-card,
	.lp-card::before,
	.lp-card__link,
	.lp-card__link svg,
	.lp-cta__btn,
	.lp-cta__btn svg {
		transition: none;
	}
}
/* ---------- 系列导航（两册） ---------- */

.lp-labs {
	display: grid;
	grid-template-columns: repeat(auto-fit, minmax(min(100%, 18rem), 1fr));
	gap: 1rem;
	margin: 0;
	padding: 0;
	list-style: none;
}

.lp-lab {
	display: flex;
	flex-direction: column;
	gap: 0.4rem;
	padding: 1.1rem 1.2rem;
	border: 1px solid var(--lp-border);
	border-radius: var(--lp-radius);
	background: var(--lp-surface);
	text-decoration: none;
	transition:
		border-color 0.2s ease,
		box-shadow 0.2s ease;
}

.lp-lab:hover {
	border-color: var(--lp-border-strong);
	box-shadow: var(--lp-shadow);
}

.lp-lab__name {
	font-size: var(--sl-text-base);
	font-weight: 650;
	color: var(--lp-fg);
}

.lp-lab__desc {
	font-size: var(--sl-text-sm);
	line-height: 1.7;
	color: var(--lp-muted);
}
</style>
