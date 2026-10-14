// Usage: node shot.cjs <cdpPort> <outPng> [hashRoute] [waitMs]
const { chromium } = require(process.env.PW_PATH || "/Users/dhruvsharma/.t3/worktrees/agent-orchestrator-1/t3-a94fcfa2/frontend/node_modules/playwright");
(async () => {
	const [port, out, route, waitMs] = process.argv.slice(2);
	const browser = await chromium.connectOverCDP(`http://127.0.0.1:${port}`);
	const pages = browser.contexts().flatMap((c) => c.pages());
	const page = pages.find((p) => !p.url().startsWith("devtools://") && /localhost|127\.0\.0\.1|app:\/\//.test(p.url())) ?? pages[0];
	if (route) {
		await page.evaluate((r) => { window.location.hash = r; }, route);
	}
	await page.waitForTimeout(Number(waitMs ?? 1500));
	await page.screenshot({ path: out });
	console.log(JSON.stringify({ url: page.url(), title: await page.title(), pages: pages.map((p) => p.url()) }));
	await browser.close();
})().catch((e) => { console.error(e); process.exit(1); });
