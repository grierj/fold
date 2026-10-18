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

export function Button({ variant, size = "md", layout, type = "button", ...rest }: Props) {
  const cls = [`btn-${variant}`, size === "sm" ? "min-h-9 px-3 text-sm" : "", layout ?? ""].filter(Boolean).join(" ");
  return <button type={type} className={cls} {...rest} />;
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
  const cls = [`btn-${variant}`, size === "sm" ? "min-h-9 px-3 text-sm" : "", layout ?? ""].filter(Boolean).join(" ");
  return (
    <Link to={to} className={cls}>
      {children}
    </Link>
  );
}
