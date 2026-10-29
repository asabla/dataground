import { type CSSProperties, type ReactNode, useId, useRef, useState } from "react";
import { mergeProps } from "react-aria/mergeProps";
import { useMove } from "react-aria/useMove";

export interface SplitViewProps {
  primaryLabel: string;
  primary: ReactNode;
  secondary?: ReactNode;
}

const minimum = 40;
const maximum = 70;
const clamp = (value: number) => Math.min(maximum, Math.max(minimum, value));

// Layout state stays local. Neither resizing nor hiding the secondary pane
// changes the identity or lifetime of the primary consumer's command state.
export function SplitView({ primaryLabel, primary, secondary }: SplitViewProps) {
  const [percent, setPercent] = useState(60);
  const frame = useRef<HTMLDivElement>(null);
  const handle = useRef<HTMLHRElement>(null);
  const primaryId = useId();
  const helpId = useId();
  const hasSecondary = secondary !== undefined && secondary !== null && secondary !== false;
  const { moveProps } = useMove({
    onMoveStart: () => handle.current?.focus(),
    onMove: (event) => {
      if (!frame.current || !handle.current) return;
      const width =
        frame.current.getBoundingClientRect().width - handle.current.getBoundingClientRect().width;
      if (width <= 0) return;
      const direction = getComputedStyle(frame.current).direction === "rtl" ? -1 : 1;
      const delta =
        event.pointerType === "keyboard" ? event.deltaX * 2 : (event.deltaX / width) * 100;
      setPercent((current) => clamp(current + delta * direction));
    },
  });
  return (
    <div className="dg-split-view">
      <div
        className="dg-split-view__layout"
        data-split={hasSecondary || undefined}
        ref={frame}
        style={
          {
            "--dg-split-primary": `${percent}fr`,
            "--dg-split-secondary": `${100 - percent}fr`,
          } as CSSProperties
        }
      >
        <section className="dg-split-view__primary" id={primaryId} aria-label={primaryLabel}>
          {primary}
        </section>
        {hasSecondary && (
          <>
            <hr
              {...mergeProps(moveProps, {
                onPointerDown: (event: React.PointerEvent<HTMLElement>) => {
                  if (event.button === 0) event.currentTarget.focus();
                },
                onMouseDown: (event: React.MouseEvent<HTMLElement>) => {
                  if (event.button === 0) event.currentTarget.focus();
                },
                onKeyDown: (event: React.KeyboardEvent) => {
                  if (event.key === "Home" || event.key === "End") {
                    event.preventDefault();
                    setPercent(event.key === "Home" ? minimum : maximum);
                  }
                },
              })}
              ref={handle}
              className="dg-split-view__handle"
              tabIndex={0}
              aria-label={primaryLabel}
              aria-controls={primaryId}
              aria-describedby={helpId}
              aria-orientation="vertical"
              aria-valuemin={minimum}
              aria-valuemax={maximum}
              aria-valuenow={Math.round(percent)}
              aria-valuetext={`${Math.round(percent)} percent`}
            />
            <span id={helpId} hidden>
              Drag or use Left and Right Arrow to resize. Home and End select the smallest and
              largest sizes.
            </span>
            <div className="dg-split-view__secondary">{secondary}</div>
          </>
        )}
      </div>
    </div>
  );
}
