<script setup lang="ts">
/**
 * 首页主体内容：四篇的路径卡片 + 三段说明 + 系列导航。
 * 样式全部以 .lp- 前缀限定作用域，定义在 style.css。
 *
 * 卡片里不写"X 课"这类数字——课时数随时会变，手写必漂移；
 * 要在首页展示规模，读 theme.idcStats（构建期从文件系统数出来）。
 */

/**
 * 首页主体内容：十个部分的路径卡片 + 三段说明 + 系列导航。
 * 样式全部以 .lp- 前缀限定作用域，定义在 style.css。
 *
 * 卡片里不写"X 课"这类数字——课时数随时会变，手写必漂移；
 * 要在首页展示规模，读 theme.idcStats（构建期从文件系统数出来）。
 */

import { withBase } from 'vitepress'

const modules = [
	{
		id: '第一部分',
		title: '认识现场',
		desc: '一个 IDC 里到底有哪些东西、后端熟悉的那套监控为什么在这块地界不够用、运维的一天在干什么、全书九个系统怎么摆。',
		href: '/00-intro/',
	},
	{
		id: '第二部分',
		title: '端口状态与设备存活',
		desc: '通不通的第一层：端口 down 的六种成因、抖动、光模块老化信号、三条采集通路，以及一万台设备怎么盯。',
		href: '/01-ports/',
	},
	{
		id: '第三部分',
		title: '主动链路探测',
		desc: '这条路现在慢了多少：探测矩阵、ECMP 让探测说谎、INT 把时延拆到每一跳，以及怎么自证清白。',
		href: '/02-probe/',
	},
	{
		id: '第四部分',
		title: '设备指标采集',
		desc: 'SNMP 的老与土：OID 与表遍历、别把设备采死、计数器回绕，再到 gNMI 订阅的范式变化。',
		href: '/03-snmp/',
	},
	{
		id: '第五部分',
		title: '流数据',
		desc: '谁在打流量：sFlow 与 NetFlow 是两回事，采样率一旦定下，你能看见多小的流就定死了。',
		href: '/04-flow/',
	},
	{
		id: '第六部分',
		title: 'CMDB、拓扑与事件',
		desc: '每条数据挂在谁身上：台账建模、两张拓扑图、IP 到端口的联查、对账与影响面。',
		href: '/05-cmdb/',
	},
	{
		id: '第七部分',
		title: '平台工程',
		desc: '从采集器到系统：十万探针的双向管控、流处理与水位线、时序表设计、一年到底多少钱。',
		href: '/06-platform/',
	},
	{
		id: '第八部分',
		title: '指标与告警',
		desc: '先立口径再立规则：同一个词三种答案、基数爆炸的账、800 条告警怎么收敛到 3 条。',
		href: '/07-alert/',
	},
	{
		id: '第九部分',
		title: '排障工具与定位',
		desc: '落到工具上：症状到工具的判断树、抓包三处对照、时间对齐、分段二分，一个 0.3% 丢包的端到端案例。',
		href: '/08-troubleshoot/',
	},
	{
		id: '第十部分',
		title: '实战：从 0 到 1',
		desc: '一个 IDC 链路质量平台怎么搭起来：约束、决策、代价、事故。实战不讲机制，只讲取舍与后果。',
		href: '/09-case/',
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
		title: '按系统分篇，不是按工序',
		body: '端口监测、SNMP 采集、流数据各是一套东西，协议、代码、失败方式全不一样。每部分收一个可复现的最小实验，默认零外部依赖，一台 Mac 就能跑。',
	},
]
</script>

<template>
	<div class="lp-content">
		<section class="lp-section" aria-labelledby="lp-path">
			<header class="lp-head">
				<p class="lp-kicker">阅读路径</p>
				<h2 id="lp-path">九个部分，一条从现场到结论的线</h2>
				<p class="lp-lede">端口、探测、SNMP、流、拓扑、平台、告警、定位——每个部分一套系统，各自有自己的协议与失败方式。每一部分最后交出一个能跑的东西。</p>
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
