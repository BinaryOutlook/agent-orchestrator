import { useState } from "react";
import { act, create, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
const { native } = vi.hoisted(() => {
	const native = { value: "", get: () => native.value, set: vi.fn((value: string) => { native.value = value; }) };
	return { native };
});
vi.mock("@expo/ui", () => ({ Host: "Host" }));
vi.mock("@expo/ui/swift-ui", () => ({
	Button: "Button", Divider: "Divider", HStack: "HStack", Image: "Image", Spacer: "Spacer",
	Text: "Text", TextField: "TextField", VStack: "VStack", useNativeState: () => native,
}));
vi.mock("@expo/ui/swift-ui/modifiers", () => Object.fromEntries([
	"buttonStyle", "controlSize", "disabled", "fixedSize", "font", "foregroundStyle", "frame",
	"multilineTextAlignment", "padding", "textFieldStyle", "tint",
].map((name) => [name, () => ({})])));
vi.mock("../glass", () => ({ glassPanel: () => ({}) }));
vi.mock("../haptics", () => ({ haptics: { select: vi.fn(), tap: vi.fn() } }));
vi.mock("../ThemeProvider", () => ({ useTheme: () => ({}), useThemeState: () => ({ scheme: "light" }) }));

import { TextField } from "@expo/ui/swift-ui";
import { ElicitationTextField } from "./elicitation-native-controls.ios";

let renderer: ReactTestRenderer | undefined;
let resetValue: (value: string | number) => void;
function Harness({ numeric = false }: { numeric?: boolean }) {
	const [value, setValue] = useState<string | number>("");
	resetValue = setValue;
	return <ElicitationTextField value={value} label="Conversation title" numeric={numeric} onChange={setValue} />;
}
beforeEach(() => { native.value = ""; native.set.mockClear(); });
afterEach(() => { if (renderer) act(() => renderer!.unmount()); renderer = undefined; });

it("does not write a delayed React acknowledgement over newer native input, but accepts external resets", () => {
	act(() => { renderer = create(<Harness />); });
	native.value = "Mobile QA verified";
	act(() => renderer!.root.findByType(TextField).props.onTextChange("Mob"));
	expect(native.value).toBe("Mobile QA verified");
	expect(native.set).not.toHaveBeenCalled();
	act(() => renderer!.root.findByType(TextField).props.onTextChange("Mobile QA verified"));
	expect(native.set).not.toHaveBeenCalled();
	act(() => resetValue(""));
	expect(native.value).toBe("");
	act(() => resetValue("Mobile QA verified"));
	expect(native.value).toBe("Mobile QA verified");
});

it("preserves numeric input while acknowledging its normalized value and applies external changes", () => {
	act(() => { renderer = create(<Harness numeric />); });
	native.value = "1.";
	act(() => renderer!.root.findByType(TextField).props.onTextChange("1."));
	expect(native.value).toBe("1.");
	expect(native.set).not.toHaveBeenCalled();
	act(() => resetValue(2));
	expect(native.value).toBe("2");
});
