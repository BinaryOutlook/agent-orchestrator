// Steer a queued message into a running Claude turn, then send a follow-up prompt.
// Usage: node steer_test.cjs <cdpPort> <aoPort> <sessionId> <evidenceDir> [loop|text] [filePrefix]
const { chromium } = require(process.env.PW_PATH || "/Users/dhruvsharma/.t3/worktrees/agent-orchestrator-1/t3-a94fcfa2/frontend/node_modules/playwright");
const fs = require("node:fs");

const [cdp, aoPort, sid, ev, mode = "loop", prefix = "steer"] = process.argv.slice(2);
const api = `http://127.0.0.1:${aoPort}/api/v1`;
const timeline = fs.createWriteStream(`${ev}/${prefix}-timeline.jsonl`, { flags: "a" });
const note = (event, extra = {}) => {
	const rec = { t: new Date().toISOString(), event, ...extra };
	console.log(JSON.stringify(rec));
	timeline.write(`${JSON.stringify(rec)}\n`);
};
const conv = async () => (await (await fetch(`${api}/sessions/${sid}/conversation`)).json());
const summary = (c) => ({
	turns: (c.turns ?? []).map((t) => ({ id: t.id?.slice(0, 8), state: t.state })),
	controller: c.controller,
	assistant: (c.messages ?? []).filter((m) => m.role === "assistant").map((m) => (m.text ?? "").slice(0, 60)).slice(-3),
});
const waitFor = async (label, pred, ms) => {
	const end = Date.now() + ms;
	while (Date.now() < end) {
		const c = await conv();
		if (pred(c)) return c;
		await new Promise((r) => setTimeout(r, 1000));
	}
	const c = await conv();
	note(`timeout:${label}`, summary(c));
	throw new Error(`timed out waiting for ${label}`);
};

(async () => {
	const browser = await chromium.connectOverCDP(`http://127.0.0.1:${cdp}`);
	const page = browser.contexts().flatMap((c) => c.pages()).find((p) => /localhost/.test(p.url()));
	await page.evaluate((r) => { window.location.hash = r; }, `/sessions/${sid}`);
	const box = page.getByRole("combobox", { name: "Message the agent" });
	await box.waitFor({ timeout: 30000 });

	// 1. The first turn is busy: running its foreground loop, or streaming its answer.
	const busy = mode === "text"
		? (c) => (c.turns ?? []).some((t) => t.state === "running") && (c.messages ?? []).some((m) => m.role === "assistant" && (m.text ?? "").length > 200)
		: (c) => (c.activities ?? []).some((a) => /seq 1 45/.test(a.summary ?? "") && a.status === "running");
	await waitFor("first turn busy", busy, 180000);
	if (mode === "loop") await new Promise((r) => setTimeout(r, 5000));
	note("first_turn_busy", summary(await conv()));

	// 2. Type a question as the user; it queues behind the running turn.
	await box.click();
	await page.keyboard.type("quick question: what is 2+2? answer with just the number", { delay: 10 });
	await page.keyboard.press("Enter");
	const steerButton = page.getByRole("button", { name: "Steer this queued message into the running turn" }).first();
	await steerButton.waitFor({ timeout: 20000 });
	await page.screenshot({ path: `${ev}/${prefix}-1-queued.png` });
	note("queued");

	// 3. Steer it into the running turn.
	await steerButton.click();
	note("steered");
	await page.waitForTimeout(3000);
	await page.screenshot({ path: `${ev}/${prefix}-2-steered.png` });

	// 4. The steered turn must settle: no turn left running.
	const settled = await waitFor("steered turn settles", (c) => (c.turns ?? []).length > 0 && (c.turns ?? []).every((t) => t.state !== "running"), 300000);
	note("steered_turn_settled", summary(settled));
	await page.waitForTimeout(2000);
	await page.screenshot({ path: `${ev}/${prefix}-3-settled.png` });

	// 5. The next prompt must run normally (the #825 rejection would fail it instantly).
	const before = (settled.turns ?? []).length;
	await box.click();
	await page.keyboard.type("reply with the single word pong", { delay: 10 });
	await page.keyboard.press("Enter");
	note("next_prompt_sent");
	const next = await waitFor("next prompt completes", (c) => (c.turns ?? []).length > before && (c.turns ?? []).every((t) => t.state !== "running"), 300000);
	note("next_prompt_done", summary(next));
	await page.waitForTimeout(2000);
	await page.screenshot({ path: `${ev}/${prefix}-4-next-prompt.png` });
	fs.writeFileSync(`${ev}/${prefix}-conversation.json`, JSON.stringify(await conv(), null, 1));
	await browser.close();
})().catch((e) => { note("error", { message: String(e?.message ?? e) }); process.exit(1); });
