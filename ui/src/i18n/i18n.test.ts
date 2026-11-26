import i18n from ".";
import { namespaces, resources } from "./resources";

describe("i18n", () => {
  it("runs English under the en-CH locale", () => {
    expect(i18n.language).toBe("en-CH");
    expect(document.documentElement.lang).toBe("en-CH");
  });

  it("serves every namespace from the en resources", () => {
    for (const ns of namespaces) {
      expect(i18n.hasResourceBundle("en", ns)).toBe(true);
    }
    expect(i18n.t("appName")).toBe(resources.en.common.appName);
    expect(i18n.t("empty", { ns: "shell" })).toBe(resources.en.shell.empty);
  });
});
