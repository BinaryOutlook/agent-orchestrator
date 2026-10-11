import { Check } from "lucide-react";
import { RadioGroup as RadioGroupPrimitive } from "radix-ui";
import { THEMES, isThemeStyle, type ThemePalette, type ThemeStyle } from "../../lib/theme-palettes";
import type { Theme } from "../../lib/theme";

/**
 * Miniature of a theme in the current light/dark mode. Colors come from the
 * palette, not tokens, because each tile previews a theme that isn't active.
 */
function ThemeSwatch({ palette }: { palette: ThemePalette }) {
	return (
		<span
			aria-hidden="true"
			className="flex h-12 w-full overflow-hidden rounded-[5px]"
			style={{ backgroundColor: palette.bg }}
		>
			<span className="h-full w-1/4" style={{ backgroundColor: palette.sidebar }} />
			<span className="flex flex-1 flex-col justify-center gap-1.5 px-2">
				<span className="h-1 w-3/4 rounded-full" style={{ backgroundColor: palette.text }} />
				<span className="h-1 w-1/2 rounded-full" style={{ backgroundColor: palette.muted }} />
				<span className="flex items-center gap-1">
					<span className="h-1.5 w-4 rounded-full" style={{ backgroundColor: palette.primary ?? palette.accent }} />
					{[palette.green, palette.yellow, palette.red].map((color) => (
						<span key={color} className="size-1.5 rounded-full" style={{ backgroundColor: color }} />
					))}
				</span>
			</span>
		</span>
	);
}

export function ThemePicker({
	value,
	mode,
	onChange,
	"aria-label": ariaLabel,
}: {
	value: ThemeStyle;
	mode: Theme;
	onChange: (value: ThemeStyle) => void;
	"aria-label": string;
}) {
	return (
		<RadioGroupPrimitive.Root
			aria-label={ariaLabel}
			value={value}
			onValueChange={(next) => {
				if (isThemeStyle(next)) onChange(next);
			}}
			className="grid w-full grid-cols-[repeat(auto-fill,minmax(7rem,1fr))] gap-2"
		>
			{THEMES.map((theme) => (
				<RadioGroupPrimitive.Item
					key={theme.id}
					value={theme.id}
					className="group flex flex-col gap-1.5 rounded-lg p-1 text-left outline-none transition-colors duration-fast hover:bg-interactive-hover focus-visible:ring-2 focus-visible:ring-ring/60"
				>
					<span className="rounded-md border border-border p-px group-data-[state=checked]:border-ring group-data-[state=checked]:ring-1 group-data-[state=checked]:ring-ring">
						<ThemeSwatch palette={theme[mode]} />
					</span>
					<span className="flex items-center justify-between gap-1 px-0.5 text-xs leading-4 text-settings-label">
						<span className="truncate">{theme.label}</span>
						<Check
							className="size-3 shrink-0 text-foreground opacity-0 group-data-[state=checked]:opacity-100"
							aria-hidden="true"
						/>
					</span>
				</RadioGroupPrimitive.Item>
			))}
		</RadioGroupPrimitive.Root>
	);
}
