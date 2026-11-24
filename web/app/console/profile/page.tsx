"use client";

import { PasswordForm } from "@/components/password-form";
import { Badge, Callout, Card, Grid, KV, Mono, PageHeader } from "@/components/ui";
import { useApp } from "@/lib/store";

export default function ProfilePage() {
  const { userName, isAdmin, auth } = useApp();

  return (
    <>
      <PageHeader title="个人设置" />
      <Grid cols={2}>
        <Card title="账户信息">
          <KV
            cols={1}
            items={[
              ["用户名", <Mono key="u">{userName}</Mono>],
              ["角色", isAdmin ? <Badge key="r" tone="brand">管理员</Badge> : <Badge key="r">普通用户</Badge>],
              ["当前会话", auth.session ? `${new Date(auth.session.created_at).toLocaleString("zh-CN")} 登录 · ${new Date(auth.session.idle_expires_at).toLocaleString("zh-CN")} 闲置到期 · ${new Date(auth.session.absolute_expires_at).toLocaleString("zh-CN")} 最迟到期` : "未知"],
            ]}
          />
          <Callout tone="info" className="mt-4">本系统账户与 Sub2API 账户相互独立，修改这里的密码不会影响 Sub2API。</Callout>
        </Card>
        <Card title="修改密码">
          <PasswordForm />
        </Card>
      </Grid>
    </>
  );
}
