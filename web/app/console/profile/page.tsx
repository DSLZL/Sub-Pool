"use client";

import { glossary } from "@/lib/glossary";
import { PasswordForm } from "@/components/password-form";
import { Badge, Callout, Card, Grid, KV, Mono, PageHeader } from "@/components/ui";
import { useApp } from "@/lib/store";
import { Timestamp } from "@/components/timestamp";

export default function ProfilePage() {
  const { userName, isAdmin, auth } = useApp();

  return (
    <>
      <PageHeader title="个人设置" desc="更改本系统密码。" helpTopic="authority" />
      <Grid cols={2}>
        <Card title="账户信息">
          <KV
            cols={1}
            items={[
              ["用户名", <Mono key="u">{userName}</Mono>],
              ["角色", isAdmin ? <Badge key="r" tone="brand">管理员</Badge> : <Badge key="r">普通用户</Badge>],
              ["当前会话", auth.session ? <><Timestamp value={auth.session.created_at} /> 登录 · <Timestamp value={auth.session.idle_expires_at} /> 闲置到期 · <Timestamp value={auth.session.absolute_expires_at} /> 最迟到期</> : "未知"],
            ]}
          />
          <Callout tone="info" className="mt-4">本系统账户与 {glossary.sub2api} 账户相互独立，修改这里的密码不会影响 {glossary.sub2api}。</Callout>
        </Card>
        <Card title="修改密码">
          <PasswordForm />
        </Card>
      </Grid>
    </>
  );
}
