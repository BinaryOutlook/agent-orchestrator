import { describe, expect, it, vi } from "vitest";
import { BrowserScreencast, jpegDimensions } from "./browser-screencast";

function jpeg(width = 640, height = 360): Buffer {
	const data = Buffer.alloc(21);
	data.set([0xff, 0xd8, 0xff, 0xc0, 0x00, 0x11, 0x08]);
	data.writeUInt16BE(height, 7);
	data.writeUInt16BE(width, 9);
	return data;
}

describe("BrowserScreencast", () => {
	it("reads JPEG dimensions without decoding the image", () => {
		expect(jpegDimensions(jpeg(1280, 720))).toEqual({ width: 1280, height: 720 });
	});

	it("starts bounded JPEG capture and acknowledges frames", async () => {
		let listener: ((event: unknown, method: string, params: Record<string, unknown>) => void) | undefined;
		const sendCommand = vi.fn(async () => undefined);
		const emit = vi.fn();
		const debug = {
			on: (_event: "message", next: typeof listener) => { listener = next; },
			off: vi.fn(),
			sendCommand,
		};
		const cast = new BrowserScreencast(debug, emit, vi.fn());
		await cast.start();
		expect(sendCommand).toHaveBeenCalledWith("Page.startScreencast", expect.objectContaining({ format: "jpeg", maxWidth: 1280, maxHeight: 720 }));
		listener?.({}, "Page.screencastFrame", { sessionId: 9, data: jpeg().toString("base64") });
		await vi.waitFor(() => expect(emit).toHaveBeenCalledWith(expect.objectContaining({ width: 640, height: 360 })));
		expect(sendCommand).toHaveBeenCalledWith("Page.screencastFrameAck", { sessionId: 9 });
		await cast.stop();
		expect(sendCommand).toHaveBeenCalledWith("Page.stopScreencast");
	});
	it("captures bounded frames when the native surface stops painting and stops on teardown", async () => {
		vi.useFakeTimers();
		try {
			const emit = vi.fn();
			let finishCapture: ((value: { data: string }) => void) | undefined;
			const sendCommand = vi.fn(async (method: string) => {
				if (method === "Page.getLayoutMetrics") return { cssVisualViewport: { pageX: 0, pageY: 400 } };
				if (method === "Page.captureScreenshot") return new Promise<{ data: string }>((resolve) => { finishCapture = resolve; });
				return undefined;
			});
			const cast = new BrowserScreencast({ on: vi.fn(), off: vi.fn(), sendCommand }, emit, vi.fn(), () => ({ width: 1920, height: 1080 }));
			await cast.start();
			await vi.advanceTimersByTimeAsync(1500);
			expect(sendCommand.mock.calls.filter(([method]) => method === "Page.captureScreenshot")).toHaveLength(1);
			expect(sendCommand).toHaveBeenCalledWith("Page.captureScreenshot", expect.objectContaining({
				captureBeyondViewport: true, clip: { x: 0, y: 400, width: 1920, height: 1080, scale: 2 / 3 },
			}));
			finishCapture?.({ data: jpeg(1280, 720).toString("base64") });
			await vi.advanceTimersByTimeAsync(0);
			expect(emit).toHaveBeenCalledWith(expect.objectContaining({ width: 1280, height: 720 }));
			await vi.advanceTimersByTimeAsync(500);
			await cast.stop();
			finishCapture?.({ data: jpeg().toString("base64") });
			await vi.advanceTimersByTimeAsync(1500);
			expect(emit).toHaveBeenCalledTimes(1);
			expect(sendCommand.mock.calls.filter(([method]) => method === "Page.captureScreenshot")).toHaveLength(2);
		} finally { vi.useRealTimers(); }
	});

});
