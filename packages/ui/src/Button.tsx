import { Button as AriaButton, type ButtonProps as AriaButtonProps } from "react-aria-components";

export type ButtonVariant = "primary" | "secondary" | "quiet" | "danger";

export interface ButtonProps extends Omit<AriaButtonProps, "className"> {
  className?: string;
  variant?: ButtonVariant;
}

export function Button({ className, variant = "secondary", ...props }: ButtonProps) {
  return (
    <AriaButton
      {...props}
      className={({ isDisabled, isFocusVisible, isHovered, isPressed }) =>
        [
          "dg-button",
          `dg-button--${variant}`,
          isHovered && "is-hovered",
          isPressed && "is-pressed",
          isFocusVisible && "is-focus-visible",
          isDisabled && "is-disabled",
          className,
        ]
          .filter(Boolean)
          .join(" ")
      }
      data-variant={variant}
    />
  );
}
