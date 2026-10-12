import { createInstance, type i18n } from "i18next";
import { initReactI18next } from "react-i18next";
import { APP_LOCALES, DEFAULT_LOCALE, type AppLocale } from "./locales";
import { enMessages } from "./messages";

export type TranslationCatalogs = Record<AppLocale, Record<string, string | string[]>>;

type Catalog = Record<string, string | string[]>;

// One lazy chunk per locale. English ships in the startup bundle as the
// fallback; the rest load only when the user's saved locale asks for them.
const localeLoaders: Record<Exclude<AppLocale, "en">, () => Promise<{ default: Catalog }>> = {
	"zh-CN": () => import("./zh-CN.json"),
	ja: () => import("./ja.json"),
	ko: () => import("./ko.json"),
	es: () => import("./es.json"),
	fr: () => import("./fr.json"),
	de: () => import("./de.json"),
	"pt-BR": () => import("./pt-BR.json"),
};

/** Create an isolated, synchronously initialized instance for unit tests. */
export function createAppI18n(
	locale: AppLocale = DEFAULT_LOCALE,
	catalogs: Partial<TranslationCatalogs> = { en: enMessages },
): i18n {
	return initializeI18n(createInstance(), locale, catalogs);
}

function initializeI18n(instance: i18n, locale: AppLocale, catalogs: Partial<TranslationCatalogs>): i18n {
	const resources = Object.fromEntries(
		APP_LOCALES.map((lng) => [lng, { translation: catalogs[lng] ?? {} }]),
	);
	void instance.init({
		lng: locale,
		fallbackLng: DEFAULT_LOCALE,
		supportedLngs: [...APP_LOCALES],
		load: "currentOnly",
		resources,
		defaultNS: "translation",
		keySeparator: false,
		nsSeparator: false,
		returnNull: false,
		initAsync: false,
		interpolation: { escapeValue: false },
	});
	return instance;
}

export const appI18n = initializeI18n(createInstance().use(initReactI18next), DEFAULT_LOCALE, { en: enMessages });

const loadedLocales = new Set<AppLocale>(["en"]);
const pendingLocales = new Map<AppLocale, Promise<void>>();

/**
 * Load a locale's catalog into appI18n. Call before changeLanguage: react-i18next
 * re-renders on languageChanged, not when a bundle is added, so the catalog has to
 * be present first. A failed load leaves English in place.
 */
export function loadLocaleCatalog(locale: AppLocale, instance: i18n = appI18n): Promise<void> {
	if (locale === "en" || loadedLocales.has(locale)) return Promise.resolve();
	const pending = pendingLocales.get(locale);
	if (pending) return pending;
	const load = localeLoaders[locale]()
		.then((module) => {
			instance.addResourceBundle(locale, "translation", module.default, true, true);
			loadedLocales.add(locale);
		})
		.catch(() => undefined)
		.finally(() => pendingLocales.delete(locale));
	pendingLocales.set(locale, load);
	return load;
}
