import { projectBadge } from "./badge";

describe("projectBadge", () => {
  it.each([
    ["riptide", "RI"],
    ["Harbor", "HA"],
    ["a", "A"],
    ["my-app", "MY"],
    ["x-ray", "XR"],
    ["_internal", "IN"],
    ["3d-viewer", "3D"],
    ["  ship  ", "SH"],
    ["ägypten", "ÄG"],
    ["--", "--"],
    ["", ""],
  ])("%j → %j", (name, badge) => {
    expect(projectBadge(name)).toBe(badge);
  });

  it("keeps a character outside the basic plane whole", () => {
    expect(projectBadge("🚀🛰")).toBe("🚀🛰");
  });
});
