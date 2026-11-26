// vitest setupFiles entry (vite.config.ts `test.setupFiles`): jest-dom's matchers on vitest's `expect`.
// Testing Library's cleanup runs by itself between tests (test.globals).
import "@testing-library/jest-dom/vitest";

// The i18n singleton for every test, as main.tsx loads it before any component
// renders: without it useTranslation renders raw keys.
import "../i18n";
