import { useId } from "react";

export interface SelectFieldProps {
  description?: string;
  errorMessage?: string;
  isDisabled?: boolean;
  isRequired?: boolean;
  label: string;
  name?: string;
  onChange?: (value: string) => void;
  options: readonly { label: string; value: string }[];
  placeholder?: string;
  value: string;
}

export function SelectField({
  description,
  errorMessage,
  isDisabled = false,
  isRequired = false,
  label,
  name,
  onChange,
  options,
  placeholder = "Choose an option",
  value,
}: SelectFieldProps) {
  const id = useId();
  const descriptionId = `${id}-description`;
  const errorId = `${id}-error`;
  const describedBy = [description && descriptionId, errorMessage && errorId]
    .filter(Boolean)
    .join(" ");
  return (
    <div
      className={["dg-select-field", isDisabled && "is-disabled", errorMessage && "is-invalid"]
        .filter(Boolean)
        .join(" ")}
    >
      <label htmlFor={id}>{label}</label>
      <select
        aria-describedby={describedBy || undefined}
        aria-invalid={errorMessage ? true : undefined}
        disabled={isDisabled}
        id={id}
        name={name}
        onChange={(event) => onChange?.(event.target.value)}
        required={isRequired}
        value={value}
      >
        <option value="">{placeholder}</option>
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
      {description && (
        <span id={descriptionId} slot="description">
          {description}
        </span>
      )}
      {errorMessage && (
        <span id={errorId} slot="errorMessage">
          {errorMessage}
        </span>
      )}
    </div>
  );
}
