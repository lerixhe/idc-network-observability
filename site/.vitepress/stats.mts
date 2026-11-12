import { readdirSync, existsSync } from 'node:fs'
import { join } from 'node:path'

/**
 * 站点对外宣称的规模数字，唯一来源。
 *
 * 课时数与实验数一律从文件系统数出来，不手写——加了课、加了 lab，
 * 首页指标条自动跟上，不会出现"README 说 24 课、实际只有 22 篇"这种漂移。
 *
 * 调用方：`config.mts` 在构建期算一次，塞进 `themeConfig.idcStats`，
 * 首页指标条通过 `useData().theme` 读取。
 */
export interface IdcStats {
	/** 课时数（各模块目录下的 .md 篇数，不含 index.md） */
	lessons: number
	/** 模块数（docs/ 下带两位数字前缀且有内容的目录） */
	modules: number
	/** 实验数（labs/ 下的子目录数） */
	labs: number
}

/** 带两位数字前缀的目录才是一个模块；`appendix`、`public` 这类不算 */
const MODULE_DIR = /^\d{2}-/

export function computeStats(docsDir: string, labsDir: string): IdcStats {
	const lessons = existsSync(docsDir)
		? readdirSync(docsDir, { withFileTypes: true })
				.filter((e) => e.isDirectory() && MODULE_DIR.test(e.name))
				.map((e) => countLessons(join(docsDir, e.name)))
				.filter((n) => n > 0)
		: []

	return {
		lessons: lessons.reduce((sum, n) => sum + n, 0),
		modules: lessons.length,
		labs: countLabDirs(labsDir),
	}
}

/** 单个模块目录里的课时数，跳过占位用的 index.md */
function countLessons(moduleDir: string): number {
	return readdirSync(moduleDir).filter((f) => f.endsWith('.md') && f !== 'index.md').length
}

/** labs/ 下每个实验一个子目录，README.md 不算 */
function countLabDirs(labsDir: string): number {
	if (!existsSync(labsDir)) return 0
	return readdirSync(labsDir, { withFileTypes: true }).filter((e) => e.isDirectory()).length
}
