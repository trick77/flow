import { useLayoutEffect, useRef } from "react";
import { useTranslation } from "react-i18next";

import type { PermissionMode, Provider } from "../contract/events.gen";
import { ActivityFold } from "./Activity";
import {
  AssistantText,
  FilesCard,
  NoticeRow,
  ResumedDivider,
  TodoList,
  UserMessage,
  WorkingIndicator,
} from "./Cards";
import type { Folded, TimelineItem } from "./fold";
import { PermissionCard } from "./PermissionCard";
import { PlanCard } from "./PlanCard";
import { QuestionCard } from "./QuestionCard";

// What the question and plan cards need of the session besides its id.
export interface CardContext {
  // The catalog's permission modes and the session's cap and mode, for the
  // mode an approved plan continues in.
  modes?: readonly PermissionMode[];
  modeCap?: PermissionMode;
  mode?: PermissionMode;
  // Hands a question over to the composer.
  onAnswerInComposer?: () => void;
}

function Item({
  item,
  sessionId,
  provider,
  ctx,
}: {
  item: TimelineItem;
  sessionId: string;
  provider?: Provider;
  ctx: CardContext;
}) {
  switch (item.type) {
    case "question":
      return (
        <QuestionCard
          item={item}
          sessionId={sessionId}
          provider={provider}
          onAnswerInComposer={ctx.onAnswerInComposer}
        />
      );
    case "plan":
      return (
        <PlanCard
          item={item}
          sessionId={sessionId}
          modes={ctx.modes}
          modeCap={ctx.modeCap}
          mode={ctx.mode}
        />
      );
    case "user":
      return <UserMessage item={item} />;
    case "fold":
      return <ActivityFold fold={item} />;
    case "text":
      return <AssistantText item={item} />;
    case "todo":
      return <TodoList item={item} />;
    case "permission":
      return (
        <PermissionCard item={item} sessionId={sessionId} provider={provider} />
      );
    case "files":
      return <FilesCard item={item} />;
    case "resumed":
      return <ResumedDivider item={item} />;
    case "notice":
      return <NoticeRow item={item} />;
  }
}

// Near the bottom: new content keeps the view pinned to it.
const STICK_PX = 80;

// Timeline renders a folded session; it follows new content while the owner
// is at the bottom and leaves the scroll alone when they read further up.
export function Timeline({
  sessionId,
  folded,
  working,
  provider,
  ...ctx
}: {
  sessionId: string;
  folded: Folded;
  working: boolean;
  provider?: Provider;
} & CardContext) {
  const { t } = useTranslation("timeline");
  const scroller = useRef<HTMLDivElement>(null);
  const stick = useRef(true);

  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  });

  return (
    <div
      className="timeline"
      ref={scroller}
      onScroll={(e) => {
        const el = e.currentTarget;
        stick.current =
          el.scrollHeight - el.scrollTop - el.clientHeight < STICK_PX;
      }}
    >
      <div className="tl" role="log" aria-label={t("label")}>
        {folded.items.map((item) => (
          <Item
            key={item.key}
            item={item}
            sessionId={sessionId}
            provider={provider ?? folded.provider}
            ctx={ctx}
          />
        ))}
        {working && (
          <WorkingIndicator tool={folded.openTool} thinking={folded.thinking} />
        )}
      </div>
    </div>
  );
}
