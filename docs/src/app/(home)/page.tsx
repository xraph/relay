import { Hero } from "@/components/landing/hero";
import { FeatureBento } from "@/components/landing/feature-bento";
import { DeliveryFlowSection } from "@/components/landing/delivery-flow-section";
import { CodeShowcase } from "@/components/landing/code-showcase";
import { CTA } from "@/components/landing/cta";

export default function HomePage() {
  return (
    <main className="flex flex-col items-center overflow-x-hidden relative">
      <Hero />
      <FeatureBento />
      <DeliveryFlowSection />
      <CodeShowcase />
      <CTA />
    </main>
  );
}
