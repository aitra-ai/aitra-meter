import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "Aitra Meter",
  description: "AI inference energy measurement dashboard",
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en">
      <body className="min-h-screen bg-gray-100 text-gray-900 antialiased">
        {children}
      </body>
    </html>
  );
}
