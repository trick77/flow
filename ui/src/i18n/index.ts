import i18n from "i18next";
import { initReactI18next } from "react-i18next";

import { LOCALE } from "./format";
import { defaultNS, namespaces, resources } from "./resources";

// English only, under the en-CH locale: i18next falls back from en-CH to the
// en resources, and its built-in number and date formatting uses en-CH.
void i18n.use(initReactI18next).init({
  resources,
  lng: LOCALE,
  fallbackLng: "en",
  ns: namespaces,
  defaultNS,
  interpolation: { escapeValue: false },
});

document.documentElement.lang = LOCALE;

export default i18n;
