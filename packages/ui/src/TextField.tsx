import {
  Input,
  Label,
  Text,
  TextArea,
  TextField as AriaTextField,
  type TextFieldProps as AriaTextFieldProps,
} from "react-aria-components";

export interface TextFieldProps
  extends Omit<
    AriaTextFieldProps,
    "children" | "className" | "defaultValue" | "onChange" | "value"
  > {
  description?: string;
  errorMessage?: string;
  isMultiline?: boolean;
  label: string;
  maxLength?: number;
  minLength?: number;
  name?: string;
  onChange?: (value: string) => void;
  value: string;
}

export function TextField({
  description,
  errorMessage,
  isInvalid,
  isMultiline = false,
  label,
  maxLength,
  minLength,
  name,
  onChange,
  value,
  ...props
}: TextFieldProps) {
  return (
    <AriaTextField
      {...props}
      className={({ isDisabled, isInvalid: fieldInvalid }) =>
        ["dg-text-field", isDisabled && "is-disabled", fieldInvalid && "is-invalid"]
          .filter(Boolean)
          .join(" ")
      }
      isInvalid={isInvalid || errorMessage !== undefined}
      onChange={onChange}
      value={value}
    >
      <Label>{label}</Label>
      {isMultiline ? (
        <TextArea maxLength={maxLength} minLength={minLength} name={name} />
      ) : (
        <Input maxLength={maxLength} minLength={minLength} name={name} />
      )}
      {description && <Text slot="description">{description}</Text>}
      {errorMessage && <Text slot="errorMessage">{errorMessage}</Text>}
    </AriaTextField>
  );
}
