import { resources } from "../i18n/resources";
import {
  ERROR_CODE_VALUES,
  PERMISSION_MODE_VALUES,
  SESSION_STATUS_VALUES,
} from "./types.gen";
import * as events from "../contract/events.gen";

describe("API types", () => {
  it("have a sentence for every error code and no stale ones", () => {
    expect(Object.keys(resources.en.errors).sort()).toEqual(
      [...ERROR_CODE_VALUES].sort(),
    );
  });

  it("agree with the event model on the shared enums", () => {
    expect(PERMISSION_MODE_VALUES).toEqual(events.PERMISSION_MODE_VALUES);
    expect(SESSION_STATUS_VALUES).toEqual(events.SESSION_STATUS_VALUES);
  });
});
