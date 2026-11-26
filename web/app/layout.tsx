import { glossary } from "@/lib/glossary";
import type { Metadata } from "next";
import "./globals.css";
import { AppProvider } from "@/lib/store";

export const metadata: Metadata = {
  title: "Sub-Pool",
  description: `${glossary.sub2api}额度池管理`,
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="zh-CN" suppressHydrationWarning>
      <body>
        <AppProvider>{children}</AppProvider>
      </body>
    </html>
  );
}
