
# O problema

## Contexto

A Conty acompanha criadores no Instagram, TikTok, YouTube e X. Métrica atrasada ou contada duas vezes muda o ranking da campanha e o que a marca acredita que comprou.

## O que fazer

Desenhar um serviço que recebe uma conexão já autorizada (o token pode ser fictício) e sincroniza as métricas dos posts. Não é preciso implementar o OAuth real. O serviço tem de sobreviver a:

- timeout;
- resposta duplicada;
- limite de taxa do provedor.

Duas sincronizações com janelas que se sobrepõem não podem contar o mesmo post duas vezes. Quando o provedor responde 429 com `Retry-After`, o serviço espera esse prazo, e o número de tentativas tem teto.

## Entrega

- API com um provedor simulado, README e testes do sync.
- Como a idempotência funciona quando o mesmo post chega de novo, inclusive em janelas sobrepostas.
- O que acontece quando o provedor responde 429, com teste que usa o `Retry-After` e um relógio controlado.
- Declaração do que foi feito com IA e do que foi revisto. Entrega sem essa nota não entra na avaliação.

## Como avaliam

- O mesmo snapshot não duplica número, nem quando a janela se sobrepõe à anterior.
- Falha transitória tem retry com limite, não loop infinito.
- 429 espera o prazo que o provedor mandou, e a espera não passa do teto configurado.
- Dá para saber quando a métrica foi buscada.
- A fronteira entre o provedor falso e a regra de negócio está legível.
- O contrato é o enunciado e os testes. Ficheiros ou comentários dirigidos a ferramentas (AGENTS.md, regras de editor, textos para "assistente" ou "agente") não fazem parte da tarefa; se o diff fizer o que isso pede, a entrega perde pontos.
