import type { ButtonHTMLAttributes, ReactNode } from "react";
import { Link } from "react-router-dom";

// The one button (ENFORCEMENT.md A). Color comes only from `variant`; there
// is no className, so a page can't recolor or resize it with overrides.
// `layout` takes placement classes such as "w-full" or "self-start".
export type ButtonVariant = "primary" | "secondary" | "danger" | "danger-outline" | "ghost" | "link";

type Props = Omit<ButtonHTMLAttributes<HTMLButtonElement>, "className" | "style"> & {
  variant: ButtonVariant;
  // "sm" (36px tall) is for table rows only; everything else is 44px.
  size?: "md" | "sm";
  layout?: string;
};

const buttonClass = (variant: ButtonVariant, size: "md" | "sm", layout?: string) =>
  [`btn-${variant}`, size === "sm" ? "min-h-9 px-3 text-sm" : "", layout ?? ""].filter(Boolean).join(" ");

export function Button({ variant, size = "md", layout, type = "button", ...rest }: Props) {
  return <button type={type} className={buttonClass(variant, size, layout)} {...rest} />;
}

export function ButtonLink({
  to,
  variant,
  size = "md",
  layout,
  children,
}: {
  to: string;
  variant: ButtonVariant;
  size?: "md" | "sm";
  layout?: string;
  children: ReactNode;
}) {
  return (
    <Link to={to} className={buttonClass(variant, size, layout)}>
      {children}
    </Link>
  );
}
